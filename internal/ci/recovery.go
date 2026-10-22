package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// The checkpoint is private executor state, not part of the public/stored Run contract.
// Replaying from its log prefix makes recovery deterministic even after a crash mid-chunk.
type jobRecovery struct {
	VM        int      `json:"vm"`
	Task      string   `json:"task"`
	Prefix    int64    `json:"prefix"`
	Hashes    []string `json:"hashes"`
	StartedAt int64    `json:"startedAt"`
	Secrets   string   `json:"secrets"` // encrypted masking snapshot, including secrets changed since start
}

func recoveryPath(run int64, job int) string {
	return filepath.Join(runDir(run), fmt.Sprintf("job-%d.recovery.json", job))
}

func readRecovery(run int64, job int) (jobRecovery, error) {
	var checkpoint jobRecovery
	b, err := os.ReadFile(recoveryPath(run, job))
	if err != nil {
		return checkpoint, err
	}
	err = json.Unmarshal(b, &checkpoint)
	if err == nil && (checkpoint.VM < 1 || checkpoint.VM > 16000 || checkpoint.Task == "" || checkpoint.Prefix < 0) {
		err = fmt.Errorf("invalid recovery checkpoint for run %d job %d", run, job)
	}
	return checkpoint, err
}

func writeRecovery(run int64, job int, checkpoint jobRecovery) error {
	b, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	path := recoveryPath(run, job)
	f, err := os.OpenFile(path+".new", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(path+".new", path)
}
