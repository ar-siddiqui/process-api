package jobs

import (
	"app/controllers"
	"app/utils"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go/service/s3"
)

// Fields are exported so that gob can access it
type AWSBatchJob struct {
	ctx       context.Context
	ctxCancel context.CancelFunc
	// Used for monitoring meta data and other routines
	wg sync.WaitGroup
	// Used for monitoring running complete for sync jobs
	wgRun sync.WaitGroup

	UUID          string `json:"jobID"`
	AWSBatchID    string
	ProcessName   string   `json:"processID"`
	Image         string   `json:"image"`
	Cmd           []string `json:"commandOverride"`
	UpdateTime    time.Time
	Status        string `json:"status"`
	apiLogs       []string
	containerLogs []string
	// results       interface{}

	JobDef   string `json:"jobDefinition"`
	JobQueue string `json:"jobQueue"`

	// Job Name in Batch for this job
	JobName       string `json:"jobName"`
	EnvVars       map[string]string
	batchContext  *controllers.AWSBatchController
	logStreamName string
	// MetaData
	ProcessVersion string
	DB             *DB
	StorageSvc     *s3.S3
	DoneChan       chan Job
}

func (j *AWSBatchJob) WaitForRunCompletion() {
	j.wgRun.Wait()
}

func (j *AWSBatchJob) JobID() string {
	return j.UUID
}

func (j *AWSBatchJob) ProcessID() string {
	return j.ProcessName
}

func (j *AWSBatchJob) ProcessVersionID() string {
	return j.ProcessVersion
}

func (j *AWSBatchJob) CMD() []string {
	return j.Cmd
}

func (j *AWSBatchJob) IMAGE() string {
	return j.Image
}

// Return current logs of the job.
// Fetches Container logs from CloudWatch.
func (j *AWSBatchJob) Logs() (JobLogs, error) {
	var logs JobLogs
	// we are fetching logs here and not in run function because we only want to fetch logs when needed
	err := j.fetchCloudWatchLogs()
	if err != nil {
		return logs, fmt.Errorf("could not get cloud watch logs")
	}

	logs.JobID = j.UUID
	logs.ProcessID = j.ProcessName
	logs.ContainerLogs = j.containerLogs
	logs.APILogs = j.apiLogs
	return logs, nil
}

func (j *AWSBatchJob) ClearOutputs() {
	// method not invoked for aysnc jobs
}

func (j *AWSBatchJob) Messages(includeErrors bool) []string {
	return j.apiLogs
}

func (j *AWSBatchJob) NewMessage(m string) {
	j.apiLogs = append(j.apiLogs, m)
}

func (j *AWSBatchJob) LastUpdate() time.Time {
	return j.UpdateTime
}

func (j *AWSBatchJob) NewStatusUpdate(status string, updateTime time.Time) {

	// If old status is one of the terminated status, it should not update status.
	switch j.Status {
	case SUCCESSFUL, DISMISSED, FAILED:
		return
	}

	j.Status = status
	if updateTime.IsZero() {
		j.UpdateTime = time.Now()
	} else {
		j.UpdateTime = updateTime
	}
	j.DB.updateJobRecord(j.UUID, status, j.UpdateTime)
}

func (j *AWSBatchJob) CurrentStatus() string {
	return j.Status
}

func (j *AWSBatchJob) ProviderID() string {
	return j.AWSBatchID
}

func (j *AWSBatchJob) Equals(job Job) bool {
	switch jj := job.(type) {
	case *AWSBatchJob:
		return j.ctx == jj.ctx
	default:
		return false
	}
}

func (j *AWSBatchJob) Create() error {
	ctx, cancelFunc := context.WithCancel(context.TODO())
	j.ctx = ctx
	j.ctxCancel = cancelFunc

	batchContext, err := controllers.NewAWSBatchController(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_REGION"))
	if err != nil {
		j.ctxCancel()
		return err
	}

	aWSBatchID, err := batchContext.JobCreate(j.ctx, j.JobDef, j.JobName, j.JobQueue, j.Cmd, j.EnvVars)
	if err != nil {
		j.ctxCancel()
		return err
	}

	j.wgRun.Add(1) // When status is one of the final status this should be decremented, this is the responsibility of who ever is updating status

	j.AWSBatchID = aWSBatchID
	j.batchContext = batchContext

	// At this point job is ready to be added to database
	err = j.DB.addJob(j.UUID, "accepted", time.Now(), "", "aws-batch", j.ProcessName)
	if err != nil {
		j.ctxCancel()
		return err
	}

	j.NewStatusUpdate(ACCEPTED, time.Time{})

	// to do defer get log stream name

	return nil
}

func (j *AWSBatchJob) Kill() error {
	j.NewMessage("Received dismiss signal")

	switch j.CurrentStatus() {
	case SUCCESSFUL, FAILED, DISMISSED:
		// if these jobs have been loaded from previous snapshot they would not have context etc
		return fmt.Errorf("can't call delete on an already completed, failed, or dismissed job")
	}

	c, err := controllers.NewAWSBatchController(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_REGION"))
	if err != nil {
		j.NewMessage("Could not send kill signal to AWS Batch API. Error: " + err.Error())
		return err
	}

	_, err = c.JobKill(j.AWSBatchID)
	if err != nil {
		j.NewMessage("Could not send kill signal to AWS Batch API. Error: " + err.Error())
		return err
	}

	j.NewStatusUpdate(DISMISSED, time.Time{})
	// If a dismiss status is updated the job is considered dismissed at this point
	// Close being graceful or not does not matter.

	defer j.Close()
	return nil
}

// Get log stream name for this job
func (j *AWSBatchJob) getLogStreamName() (err error) {
	c, err := controllers.NewAWSBatchController(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_DEFAULT_REGION"))
	if err != nil {
		return
	}

	_, logStreamName, err := c.JobMonitor(j.AWSBatchID)
	if err != nil {
		return
	}
	j.logStreamName = logStreamName
	return
}

// Fetches logs from CloudWatch using the AWS Go SDK
func (j *AWSBatchJob) fetchCloudWatchLogs() (err error) {
	if j.logStreamName == "" {
		err = j.getLogStreamName()
		if err != nil {
			return fmt.Errorf("could not get log stream name")
		}
	}

	if j.logStreamName == "" {
		return fmt.Errorf("logStreamName is empty. If you just ran your job, retry in few seconds")
	}

	// Create a new session in the desired region
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(os.Getenv("AWS_REGION")),
	})
	if err != nil {
		return fmt.Errorf("Error creating session: " + err.Error())
	}

	// Create a CloudWatchLogs client
	svc := cloudwatchlogs.New(sess)

	// Define the parameters for the log stream you want to read
	params := &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(os.Getenv("BATCH_LOG_STREAM_GROUP")),
		LogStreamName: aws.String(j.logStreamName),
		StartFromHead: aws.Bool(true),
	}

	// Call the GetLogEvents API to read the log events
	resp, err := svc.GetLogEvents(params)
	if err != nil {
		if err.Error() == "ResourceNotFoundException: The specified log stream does not exist." {
			return nil
		} else {
			return err
		}
	}

	// Print the log events
	logs := make([]string, len(resp.Events))
	for i, event := range resp.Events {
		logs[i] = *event.Message
	}
	j.containerLogs = logs
	return nil
}

// Write metadata at the job's metadata location
func (j *AWSBatchJob) WriteMetaData() {
	j.NewMessage("Starting metadata writing routine.")
	j.wg.Add(1)
	defer j.wg.Done()
	defer j.NewMessage("Finished metadata writing routine.")

	c, err := controllers.NewAWSBatchController(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_REGION"))
	if err != nil {
		j.NewMessage(fmt.Sprintf("error writing metadata: %s", err.Error()))
		return
	}

	imgURI, err := c.GetImageURI(j.JobDef)
	if err != nil {
		j.NewMessage(fmt.Sprintf("error writing metadata: %s", err.Error()))
		return
	}

	// imgDgst would be incorrect if tag has been updated in between
	// if there are multiple architechture available for same image tag
	var imgDgst string
	if strings.Contains(imgURI, "amazonaws.com/") {
		imgDgst, err = getECRImageDigest(imgURI)
		if err != nil {
			j.NewMessage(fmt.Sprintf("error writing metadata: %s", err.Error()))
			return
		}
	} else {
		imgDgst, err = getDkrHubImageDigest(imgURI, "dummy")
		if err != nil {
			j.NewMessage(fmt.Sprintf("error writing metadata: %s", err.Error()))
			return
		}
	}

	p := process{j.ProcessID(), j.ProcessVersion}
	i := image{imgURI, imgDgst}

	g, s, e, err := c.GetJobTimes(j.AWSBatchID)
	if err != nil {
		j.NewMessage(fmt.Sprintf("error writing metadata: %s", err.Error()))
		return
	}

	md := metaData{
		Context:         "https://github.com/Dewberry/process-api/blob/main/context.jsonld",
		JobID:           j.UUID,
		Process:         p,
		Image:           i,
		Commands:        j.Cmd,
		GeneratedAtTime: g,
		StartedAtTime:   s,
		EndedAtTime:     e,
	}

	jsonBytes, err := json.Marshal(md)
	if err != nil {
		j.NewMessage(fmt.Sprintf("error writing metadata: %s", err.Error()))
		return
	}

	metadataDir := os.Getenv("STORAGE_METADATA_DIR")
	mdLocation := fmt.Sprintf("%s/%s.json", metadataDir, j.UUID)
	// TODO: Determine if batch metadata should be put on aws...currently this is the case
	utils.WriteToS3(j.StorageSvc, jsonBytes, mdLocation, "application/json", 0)
}

// func (j *AWSBatchJob) WriteResults(data []byte) (err error) {
// 	j.NewMessage("Starting results writing routine.")
// 	defer j.NewMessage("Finished results writing routine.")

// 	resultsDir := os.Getenv("STORAGE_RESULTS_DIR")
// 	resultsLocation := fmt.Sprintf("%s/%s.json", resultsDir, j.UUID)
// 	err = utils.WriteToS3(j.StorageSvc, data, resultsLocation, "application/json", 0)
// 	if err != nil {
// 		j.NewMessage(fmt.Sprintf("Error writing results to storage: %v", err.Error()))
// 	}
// 	return
// }

func (j *AWSBatchJob) RunFinished() {
	j.wgRun.Done()
}

// Write final logs, cancelCtx, write metadata
func (j *AWSBatchJob) Close() {
	// to do: add panic recover to remove job from active jobs even if following panics
	j.ctxCancel()

	err := j.fetchCloudWatchLogs()
	if err != nil {
		j.NewMessage("Could not fetch cloud watch logs. Error: " + err.Error())
	}
	j.wg.Wait() // wait if other routines like metadata are running because they can send logs
	// this should be completed before job is sent to Done because logs are handled differently for active and unactive jobs
	j.DB.upsertLogs(j.UUID, j.ProcessID(), j.apiLogs, j.containerLogs)
	j.DoneChan <- j // At this point job can be safely removed from active jobs
}