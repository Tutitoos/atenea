package kivgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Tutitoos/atenea/internal/pidlock"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// MaintenanceJob is persistent diagnostic state, not a graph freshness claim.
type MaintenanceJob struct {
	ActiveRepository string    `json:"active_repository,omitempty"`
	Detail           string    `json:"detail,omitempty"`
	InputDigest      string    `json:"input_digest,omitempty"`
	ID               string    `json:"id"`
	State            string    `json:"state"`
	Phase            string    `json:"phase"`
	Repository       string    `json:"repository"`
	Generation       int       `json:"generation"`
	Updated          time.Time `json:"updated"`
	Code             string    `json:"error_code,omitempty"`
	Reason           string    `json:"reason,omitempty"`
}

type maintenanceCompletion struct {
	done chan struct{}
	err  error
}

type maintenanceJobs struct {
	completion *maintenanceCompletion
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	done       chan struct{}
	job        MaintenanceJob
	path       string
	err        error
}

func maintenanceFailure(code, message string) *contract.Failure {
	return &contract.Failure{Kind: contract.FailureUnavailable, Code: code, Message: message, HealthNeutral: true}
}

// EnableBackground gives jobs a service lifetime independent of query contexts.
func (r *Runner) EnableBackground() error {
	ctx, cancel := context.WithCancel(context.Background())
	m := &maintenanceJobs{ctx: ctx, cancel: cancel, path: filepath.Join(r.maintenanceDirectory, "job.json")}
	if raw, err := os.ReadFile(m.path); err == nil {
		if err = json.Unmarshal(raw, &m.job); err != nil {
			cancel()
			return fmt.Errorf("maintenance state: %w", err)
		}
		if m.job.State == "queued" || m.job.State == "running" || m.job.State == "verifying" {
			release, err := pidlock.Claim(filepath.Join(r.maintenanceDirectory, "job.lock"))
			if err == nil {
				m.job.State, m.job.Code, m.job.Reason = "interrupted", "maintenance_interrupted", "Previous service stopped before recording completion; freshness must be verified."
				err = m.save()
				release()
				if err != nil {
					cancel()
					return err
				}
			} else if !errors.Is(err, pidlock.ErrHeld) {
				cancel()
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		cancel()
		return err
	}
	r.jobs = m
	return nil
}

// CloseMaintenance cancels owned children and waits until their state is saved.
func (r *Runner) CloseMaintenance() {
	if r.jobs != nil {
		r.jobs.cancel()
		r.jobs.wg.Wait()
	}
}

// Maintenance reads current persisted state, including jobs owned elsewhere.
func (r *Runner) Maintenance() (MaintenanceJob, error) { return r.MaintenanceID("") }

// MaintenanceID reads a persisted job by ID, or the current job when ID is empty.
func (r *Runner) MaintenanceID(id string) (MaintenanceJob, error) {
	var job MaintenanceJob
	filename := filepath.Join(r.maintenanceDirectory, "job.json")
	if id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return job, contract.Fail(contract.FailureInvalidInput, "invalid maintenance job identifier")
		}
		filename = filepath.Join(r.maintenanceDirectory, "jobs", id+".json")
	}
	raw, err := os.ReadFile(filename)
	if os.IsNotExist(err) && id == "" {
		job.State = "idle"
		return job, nil
	}
	if err != nil {
		return job, err
	}
	err = json.Unmarshal(raw, &job)
	return job, err
}

func (m *maintenanceJobs) save() error {
	m.job.Updated = time.Now().UTC()
	raw, err := json.Marshal(m.job)
	if err != nil {
		return err
	}
	if err := atomicJobWrite(m.path, raw); err != nil {
		return err
	}
	if m.job.ID != "" {
		return atomicJobWrite(filepath.Join(filepath.Dir(m.path), "jobs", m.job.ID+".json"), raw)
	}
	return nil
}

func atomicJobWrite(filename string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".job-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}

func (r *Runner) managedFresh(ctx context.Context, sess Session, status *statusResult, req contract.RunRequest) (*statusResult, bool, error) {
	explicit := req.Capability.ID == CapabilityEnsureFresh
	if contentFresh(status) && !explicit {
		return status, false, nil
	}
	if !explicit && !r.autoReindexRegistered {
		return nil, false, maintenanceFailure("freshness_unverified", "Graph freshness is unverified; request graph.ensure_fresh.")
	}
	if r.jobs == nil {
		if explicit {
			return r.ensureFresh(ctx, sess, status, req)
		}
		return nil, false, maintenanceFailure("maintenance_service_required", "Automatic rebuild requires the Atenea service; start it or request graph.ensure_fresh explicitly.")
	}
	m := r.jobs
	for {
		m.mu.Lock()
		if m.ctx.Err() != nil {
			m.mu.Unlock()
			return nil, false, maintenanceFailure("maintenance_interrupted", "Atenea is shutting down")
		}
		if m.done == nil {
			release, err := pidlock.Claim(filepath.Join(r.maintenanceDirectory, "job.lock"))
			if errors.Is(err, pidlock.ErrHeld) {
				m.mu.Unlock()
				job, e := r.Maintenance()
				if e != nil {
					return nil, false, e
				}
				if !explicit {
					return nil, false, maintenanceFailure("maintenance_pending", "Kivgraph maintenance pending; job "+job.ID)
				}
				select {
				case <-ctx.Done():
					return nil, false, ctx.Err()
				case <-time.After(100 * time.Millisecond):
					fresh, err := r.fetchStatus(ctx, sess)
					if err != nil {
						return nil, false, err
					}
					if contentFresh(fresh) {
						return fresh, true, nil
					}
					status = fresh
					continue
				}
			}
			if err != nil {
				m.mu.Unlock()
				return nil, false, err
			}
			generation := 0
			if status != nil {
				generation = status.SnapshotID
			}
			inputDigest := ""
			if status != nil && status.ContentFreshness != nil {
				inputDigest = status.ContentFreshness.InputDigest
			}
			if !explicit && m.job.Generation == generation && (m.job.InputDigest == "" || m.job.InputDigest == inputDigest) && (m.job.State == "failed" || m.job.State == "interrupted" || m.job.State == "canceled") {
				job := m.job
				release()
				m.mu.Unlock()
				return nil, false, maintenanceFailure("rebuild_blocked", "Job "+job.ID+" failed for this generation; inspect maintenance and retry graph.ensure_fresh explicitly. "+job.Reason)
			}
			m.job = MaintenanceJob{ID: uuid.NewString(), State: "queued", Phase: "freshness", Repository: req.Repository.ID, Generation: generation, InputDigest: inputDigest}
			if err = m.save(); err != nil {
				release()
				m.mu.Unlock()
				return nil, false, err
			}
			m.done = make(chan struct{})
			completion := &maintenanceCompletion{done: m.done}
			m.completion = completion
			m.err = nil
			m.wg.Add(1)
			go func() {
				defer m.wg.Done()
				jobCtx, cancel := context.WithTimeout(m.ctx, r.indexTimeout)
				defer cancel()
				m.mu.Lock()
				m.job.State = "running"
				m.err = m.save()
				initialErr := m.err
				m.mu.Unlock()
				var fresh *statusResult
				err := initialErr
				if err == nil {
					ownSession, e := r.session(jobCtx)
					err = e
					if err == nil {
						fresh, _, err = r.ensureFresh(context.WithValue(jobCtx, maintenancePhaseKey{}, m), ownSession, status, req)
					}
				}
				if err == nil && r.OnMaintenanceVerified != nil {
					r.OnMaintenanceVerified()
				}
				m.mu.Lock()
				defer m.mu.Unlock()
				m.err = err
				m.job.State = "succeeded"
				if err == nil {
					m.job.Phase = "complete"
				}
				if fresh != nil {
					m.job.Generation = fresh.SnapshotID
				}
				if err != nil {
					m.job.State = "failed"
					m.job.Code = contract.CodeOf(err)
					m.job.Reason = sanitizedMaintenanceReason(err.Error())
					if contract.KindOf(err) == contract.FailureCanceled || errors.Is(err, context.Canceled) {
						m.job.State = "canceled"
					}
					if m.ctx.Err() != nil {
						m.job.State = "interrupted"
						m.job.Code = "maintenance_interrupted"
					}
				}
				if e := m.save(); e != nil {
					m.err = e
				}
				completion.err = m.err
				release()
				close(completion.done)
				m.done = nil
			}()
		}
		done, job, completion := m.done, m.job, m.completion
		m.mu.Unlock()
		if !explicit {
			return nil, false, maintenanceFailure("maintenance_pending", "Kivgraph maintenance pending; job "+job.ID+". Query atenea.command name=maintenance.")
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-done:
		}
		m.mu.Lock()
		err := completion.err
		m.mu.Unlock()
		if err != nil {
			return nil, false, err
		}
		fresh, err := r.fetchStatus(ctx, sess)
		if err == nil && !contentFresh(fresh) {
			err = maintenanceFailure("freshness_unverified", "Completed maintenance is no longer fresh; results withheld")
		}
		return fresh, true, err
	}
}

type maintenancePhaseKey struct{}

func noteMaintenancePhase(ctx context.Context, phase string) error {
	if m, ok := ctx.Value(maintenancePhaseKey{}).(*maintenanceJobs); ok {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.job.Phase = phase
		if phase == "verifying" {
			m.job.State = "verifying"
		}
		return m.save()
	}
	return nil
}

func sanitizedMaintenanceReason(reason string) string {
	reason = contract.RedactRaw(reason)
	if len(reason) > 2048 {
		reason = reason[:2048] + "..."
	}
	return reason
}

// Progress names the unit actually reported by the full indexer. It is kept
// separate from the requesting repository because a full corpus is shared.
func noteMaintenanceProgress(ctx context.Context, progress indexProgress) error {
	if m, ok := ctx.Value(maintenancePhaseKey{}).(*maintenanceJobs); ok {
		m.mu.Lock()
		defer m.mu.Unlock()
		phase, repository, detail := sanitizedMaintenanceReason(progress.Phase), sanitizedMaintenanceReason(progress.Repository), sanitizedMaintenanceReason(progress.Detail)
		if m.job.Phase == phase && m.job.ActiveRepository == repository && m.job.Detail == detail {
			return nil
		}
		m.job.Phase, m.job.ActiveRepository, m.job.Detail = phase, repository, detail
		return m.save()
	}
	return nil
}
