package daemon

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// JobStatus enumerates lifecycle states for a detached turn job.
const (
	JobRunning  = "running"
	JobDone     = "done"
	JobFailed   = "failed"
	JobCanceled = "canceled"
)

// Job tracks a detached agent turn. ID is session+seq (job counter per session).
type Job struct {
	ID        string
	SessionID string
	Seq       int
	Status    string
	CreatedAt int64
	UpdatedAt int64
	Error     string
	StartSeq  int // message seq at turn start; follow polls since StartSeq

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func (j *Job) toResult() JobResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JobResult{
		ID:        j.ID,
		SessionID: j.SessionID,
		Seq:       j.Seq,
		Status:    j.Status,
		CreatedAt: j.CreatedAt,
		UpdatedAt: j.UpdatedAt,
		Error:     j.Error,
		StartSeq:  j.StartSeq,
	}
}

// job helpers on SessionManager

func (m *SessionManager) nextJobSeq(sessionID string) int {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	if m.jobSeq == nil {
		m.jobSeq = make(map[string]int)
	}
	m.jobSeq[sessionID]++
	return m.jobSeq[sessionID]
}

func (m *SessionManager) registerJob(sessionID string, startSeq int, cancel context.CancelFunc) *Job {
	seq := m.nextJobSeq(sessionID)
	id := fmt.Sprintf("%s:%d", sessionID, seq)
	now := time.Now().UnixMilli()
	job := &Job{
		ID:        id,
		SessionID: sessionID,
		Seq:       seq,
		Status:    JobRunning,
		CreatedAt: now,
		UpdatedAt: now,
		StartSeq:  startSeq,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	m.jobsMu.Lock()
	if m.jobs == nil {
		m.jobs = make(map[string]*Job)
	}
	m.jobs[id] = job
	m.jobsMu.Unlock()
	return job
}

func (m *SessionManager) finishJob(job *Job, err error, halted bool) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.Status != JobRunning {
		return
	}
	if err != nil {
		if err == context.Canceled || halted {
			job.Status = JobCanceled
		} else {
			job.Status = JobFailed
		}
		job.Error = err.Error()
	} else {
		job.Status = JobDone
	}
	job.UpdatedAt = time.Now().UnixMilli()
	select {
	case <-job.done:
	default:
		close(job.done)
	}
}

// ListJobs returns a snapshot of all jobs sorted by CreatedAt descending.
func (m *SessionManager) ListJobs() []JobResult {
	m.jobsMu.RLock()
	defer m.jobsMu.RUnlock()
	out := make([]JobResult, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j.toResult())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// GetJob returns a job by ID.
func (m *SessionManager) GetJob(id string) (JobResult, bool) {
	m.jobsMu.RLock()
	j, ok := m.jobs[id]
	m.jobsMu.RUnlock()
	if !ok {
		return JobResult{}, false
	}
	return j.toResult(), true
}

// CancelJob cancels a running job by ID. It cancels the detached turn context
// which propagates through the agent loop via context cancellation.
func (m *SessionManager) CancelJob(id string) (JobResult, error) {
	m.jobsMu.RLock()
	j, ok := m.jobs[id]
	m.jobsMu.RUnlock()
	if !ok {
		return JobResult{}, fmt.Errorf("job not found: %s", id)
	}
	j.mu.Lock()
	running := j.Status == JobRunning
	cancel := j.cancel
	j.mu.Unlock()
	if !running {
		return j.toResult(), nil
	}
	if cancel != nil {
		cancel()
	}
	// Mark canceled optimistically; finishJob will not overwrite.
	j.mu.Lock()
	if j.Status == JobRunning {
		j.Status = JobCanceled
		j.Error = "canceled by user"
		j.UpdatedAt = time.Now().UnixMilli()
		select {
		case <-j.done:
		default:
			close(j.done)
		}
	}
	j.mu.Unlock()
	// Also halt session emergency to ensure agent loop observes halt? Use TurnCancel already.
	// No need to persist halt metadata for job cancel; just context cancel.
	return j.toResult(), nil
}
