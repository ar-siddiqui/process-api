package jobs

import (
	"sync"
)

// It is the resoponsibility of originator to add and remove job from ActiveJobs
type ActiveJobs struct {
	Jobs map[string]*Job `json:"jobs"`
	mu   sync.Mutex
}

func (ac *ActiveJobs) Add(j *Job) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	ac.Jobs[(*j).JobID()] = j
}

func (ac *ActiveJobs) Remove(j *Job) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	delete(ac.Jobs, (*j).JobID())
}

// Revised to kill only currently active jobs
func (ac *ActiveJobs) KillAll() error {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	for _, j := range ac.Jobs {
		if (*j).CurrentStatus() == ACCEPTED || (*j).CurrentStatus() == RUNNING {
			if err := (*j).Kill(); err != nil {
				return err
			}
		}
	}
	return nil
}