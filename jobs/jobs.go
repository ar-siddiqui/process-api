package jobs

import (
	"app/utils"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go/service/s3"
)

type Resources struct {
	CPUs   float32
	Memory int
}

// Job refers to any process that has been created through
// the processes/{processID}/execution endpoint
type Job interface {
	CMD() []string
	CurrentStatus() string
	Equals(Job) bool
	IMAGE() string
	JobID() string
	ProcessID() string
	Logs() (JobLogs, error)
	Kill() error
	LastUpdate() time.Time
	Messages(bool) []string
	NewMessage(string)
	NewStatusUpdate(string)
	Run()
	Create() error
	GetSizeinCache() int
}

// JobStatus contains details about a job
// only those fields are exported which are part of OGC status response
type JobStatus struct {
	JobID      string    `json:"jobID"`
	LastUpdate time.Time `json:"updated"`
	Status     string    `json:"status"`
	ProcessID  string    `json:"processID"`
	Type       string    `default:"process" json:"type"`
	host       string
	mode       int
}

// JobLogs describes logs for the job
type JobLogs struct {
	ContainerLog []string `json:"container_log"`
	APILog       []string `json:"api_log"`
}

// OGCStatusCodes
const (
	ACCEPTED   = "accepted"
	RUNNING    = "running"
	SUCCESSFUL = "successful"
	FAILED     = "failed"
	DISMISSED  = "dismissed"
)

// Returns an array of all Job statuses in memory
// Most recently updated job first
func (ac *ActiveJobs) ListJobs() []JobStatus {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	jobs := make([]JobStatus, len(ac.Jobs))

	var i int
	for _, j := range ac.Jobs {
		js := JobStatus{
			ProcessID:  (*j).ProcessID(),
			JobID:      (*j).JobID(),
			LastUpdate: (*j).LastUpdate(),
			Status:     (*j).CurrentStatus(),
		}
		jobs[i] = js
		i++
	}

	// sort the jobs in order with most recent time first
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].LastUpdate.After(jobs[j].LastUpdate)
	})

	return jobs
}

// If JobID exists but results file doesn't then it raises an error
// Assumes jobID is valid
func FetchResults(svc *s3.S3, jid string) (interface{}, error) {
	key := fmt.Sprintf("%s/%s.json", os.Getenv("S3_RESULTS_DIR"), jid)

	exist, err := utils.KeyExists(key, svc)
	if err != nil {
		return nil, err
	}

	if !exist {
		return nil, fmt.Errorf("not found")
	}

	data, err := utils.GetS3JsonData(key, svc)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// If JobID exists but metadata file doesn't then it raises an error
// Assumes jobID is valid
func FetchMeta(svc *s3.S3, jid string) (interface{}, error) {
	key := fmt.Sprintf("%s/%s.json", os.Getenv("S3_META_DIR"), jid)

	exist, err := utils.KeyExists(key, svc)
	if err != nil {
		return nil, err
	}

	if !exist {
		return nil, fmt.Errorf("not found")
	}

	data, err := utils.GetS3JsonData(key, svc)
	if err != nil {
		return nil, err
	}

	return data, nil
}
