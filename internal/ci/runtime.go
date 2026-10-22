package ci

import (
	"context"
	"fmt"

	"spikeloader/internal/db"
	"spikeloader/internal/sshx"
)

// jobExecutor keeps scheduling independent of git, SSH and microVM lifecycle.
type jobExecutor interface {
	Prepare(context.Context, *Run) (*workspace, error)
	Run(context.Context, jobContext) int
	Remove(context.Context, int) error
}

type vmExecutor struct{ engine *Engine }

func (v vmExecutor) Prepare(ctx context.Context, r *Run) (*workspace, error) {
	return v.engine.workspace(ctx, r)
}
func (vmExecutor) Run(ctx context.Context, jc jobContext) int { return runJob(ctx, jc) }
func (vmExecutor) Remove(ctx context.Context, id int) error {
	return sshx.AgentJSON(ctx, "DELETE", fmt.Sprintf("/vms/%d", id), nil, nil)
}
func (e *Engine) execution() jobExecutor {
	if e.executor != nil {
		return e.executor
	}
	return vmExecutor{e}
}

type runStore interface {
	Save(*Run)
	Track(*Run, []byte) <-chan error
	Delete(int64)
}
type postgresRuns struct{}

func (postgresRuns) Save(r *Run) { saveRun(r) }
func (postgresRuns) Track(r *Run, snapshot []byte) <-chan error {
	return db.WriteTracked(saveRunSQL, r.ID, r.Project, r.Status, snapshot)
}
func (postgresRuns) Delete(id int64) { deleteRun(id) }
func (e *Engine) storage() runStore {
	if e.runStore != nil {
		return e.runStore
	}
	return postgresRuns{}
}
