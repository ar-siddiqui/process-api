package jobs

import (
	"app/controllers"
	"context"
	"os"
	"time"
	"unsafe"

	"github.com/labstack/gommon/log"
)

type AWSBatchJob struct {
	Ctx           context.Context
	CtxCancel     context.CancelFunc
	UUID          string `json:"jobID"`
	AWSBatchID    string
	ProcessName   string   `json:"processID"`
	ImgTag        string   `json:"imageAndTag"`
	Cmd           []string `json:"commandOverride"`
	UpdateTime    time.Time
	Status        string `json:"status"`
	APILogs       []string
	ContainerLogs []string
	Links         []Link `json:"links"`

	JobDef       string `json:"jobDefinition"`
	JobQueue     string `json:"jobQueue"`
	JobName      string `json:"jobName"`
	EnvVars      map[string]string
	BatchContext *controllers.AWSBatchController
}

func (j *AWSBatchJob) JobID() string {
	return j.UUID
}

func (j *AWSBatchJob) ProcessID() string {
	return j.ProcessName
}

func (j *AWSBatchJob) CMD() []string {
	return j.Cmd
}

func (j *AWSBatchJob) IMGTAG() string {
	return j.ImgTag
}

func (j *AWSBatchJob) Logs() map[string][]string {
	l := make(map[string][]string)
	l["Container Logs"] = j.ContainerLogs
	l["API Logs"] = j.APILogs
	return l
}

func (j *AWSBatchJob) ClearOutputs() {
	// method not invoked for aysnc jobs
}

func (j *AWSBatchJob) Messages(includeErrors bool) []string {
	return j.APILogs
}

func (j *AWSBatchJob) NewMessage(m string) {
	j.APILogs = append(j.APILogs, m)
}

func (j *AWSBatchJob) HandleError(m string) {
	j.APILogs = append(j.APILogs, m)
	j.NewStatusUpdate(FAILED)
	j.CtxCancel()
}

func (j *AWSBatchJob) LastUpdate() time.Time {
	return j.UpdateTime
}

func (j *AWSBatchJob) NewStatusUpdate(s string) {
	j.Status = s
	j.UpdateTime = time.Now()
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
		return j.Ctx == jj.Ctx
	default:
		return false
	}
}

// set withLogs to false for batch jobs, logs can be retrieved from cloudwatch
func (j *AWSBatchJob) Create() error {
	ctx, cancelFunc := context.WithCancel(j.Ctx)
	j.Ctx = ctx
	j.CtxCancel = cancelFunc

	batchContext, err := controllers.NewAWSBatchController(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_DEFAULT_REGION"))
	if err != nil {
		j.HandleError(err.Error())
		return err
	}

	log.Debug("j.JobDef | ", j.JobDef)
	log.Debug("j.JobQueue | ", j.JobQueue)
	log.Debug("j.JobName  | ", j.JobName)
	aWSBatchID, err := batchContext.JobCreate(j.Ctx, j.JobDef, j.JobName, j.JobQueue, j.Cmd, j.EnvVars)
	if err != nil {
		j.HandleError(err.Error())
		return err
	}

	j.AWSBatchID = aWSBatchID
	j.BatchContext = batchContext

	// verify command in body
	if j.Cmd == nil {
		j.HandleError(err.Error())
		return err
	}

	j.NewStatusUpdate(ACCEPTED)
	return nil
}

// set withLogs to false for batch jobs, logs can be retrieved from cloudwatch
func (j *AWSBatchJob) Run() {
	c, err := controllers.NewAWSBatchController(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_DEFAULT_REGION"))
	if err != nil {
		j.HandleError(err.Error())
		return
	}

	if j.AWSBatchID == "" {
		j.HandleError("AWSBatchID empty")
		return
	}

	var oldStatus string

	for {
		status, logStream, err := c.JobMonitor(j.AWSBatchID)
		if err != nil {
			j.HandleError(err.Error())
			return
		}
		// j.ContainerLogs = append(j.ContainerLogs, logStream)
		j.ContainerLogs = []string{logStream}

		if status != oldStatus {
			switch status {
			case "ACCEPTED":
				j.NewStatusUpdate(ACCEPTED)
			case "RUNNING":
				j.NewStatusUpdate(RUNNING)
			case "SUCCEEDED":
				// fetch results here // todo
				j.NewStatusUpdate(SUCCESSFUL)
				j.CtxCancel()
				return
			case "DISMISSED":
				j.NewStatusUpdate(DISMISSED)
				j.CtxCancel()
				return
			case "FAILED":
				j.NewStatusUpdate(FAILED)
				j.CtxCancel()
				return
			}
		}
		oldStatus = status
		time.Sleep(10 * time.Second)
	}
}

func (j *AWSBatchJob) Kill() error {
	c, err := controllers.NewAWSBatchController(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_DEFAULT_REGION"))
	if err != nil {
		j.HandleError(err.Error())
		return err
	}

	_, err = c.JobKill(j.AWSBatchID)
	if err != nil {
		return err
	}

	j.NewStatusUpdate(DISMISSED)
	j.CtxCancel()
	return nil
}

// Placeholder
func (j *AWSBatchJob) GetSizeinCache() int {
	cmdData := int(unsafe.Sizeof(j.Cmd))
	for _, item := range j.Cmd {
		cmdData += len(item)
	}

	messageData := int(unsafe.Sizeof(j.APILogs))
	for _, item := range j.APILogs {
		messageData += len(item)
	}

	// not calculated appropriately, add method...
	linkData := int(unsafe.Sizeof(j.Links))

	totalMemory := cmdData + messageData + linkData +
		int(unsafe.Sizeof(j.Ctx)) +
		int(unsafe.Sizeof(j.CtxCancel)) +
		int(unsafe.Sizeof(j.UUID)) + len(j.UUID) +
		int(unsafe.Sizeof(j.AWSBatchID)) + len(j.AWSBatchID) +
		int(unsafe.Sizeof(j.ImgTag)) + len(j.ImgTag) +
		int(unsafe.Sizeof(j.UpdateTime)) +
		int(unsafe.Sizeof(j.Status)) +
		int(unsafe.Sizeof(j.ContainerLogs)) + len(j.ContainerLogs) +
		int(unsafe.Sizeof(j.JobDef)) + len(j.JobDef) +
		int(unsafe.Sizeof(j.JobQueue)) + len(j.JobQueue) +
		int(unsafe.Sizeof(j.JobName)) + len(j.JobName) +
		int(unsafe.Sizeof(j.EnvVars)) + len(j.EnvVars)

	return totalMemory
}
