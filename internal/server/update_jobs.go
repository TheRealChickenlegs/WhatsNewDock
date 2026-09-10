package server

import (
	"net/http"
	"sync"
	"time"
)

type updateJobStatus string

const (
	jobQueued     updateJobStatus = "queued"
	jobPulling    updateJobStatus = "pulling"
	jobRecreating updateJobStatus = "recreating"
	jobDone       updateJobStatus = "done"
	jobFailed     updateJobStatus = "failed"
)

// updateJob describes the in-flight state of a container update.
type updateJob struct {
	ContainerID string          `json:"container_id"`
	Name        string          `json:"name"`
	Status      updateJobStatus `json:"status"`
	Progress    int             `json:"progress"` // 0-100, or -1 when indeterminate
	Message     string          `json:"message"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// jobTracker keeps in-memory update-job state keyed by container id.
type jobTracker struct {
	mu   sync.Mutex
	jobs map[string]*updateJob
}

func newJobTracker() *jobTracker {
	return &jobTracker{jobs: map[string]*updateJob{}}
}

func (t *jobTracker) set(id string, j *updateJob) {
	j.UpdatedAt = time.Now().UTC()
	t.mu.Lock()
	t.jobs[id] = j
	t.mu.Unlock()
}

func (t *jobTracker) get(id string) *updateJob {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.jobs[id]
}

// handleUpdateStatus returns the current update job for a container.
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.get(r.PathValue("id"))
	if job == nil {
		writeError(w, http.StatusNotFound, "no active update")
		return
	}
	writeJSON(w, http.StatusOK, job)
}
