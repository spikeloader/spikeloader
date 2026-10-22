package fcagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"spikeloader/internal/agent"
	"spikeloader/internal/loglimit"
	"spikeloader/internal/sshx"
)

type task struct {
	state   agent.JobState
	changed chan struct{}
	cancel  context.CancelFunc
}

var taskID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

func (s *Server) taskDir(id string) string { return filepath.Join(s.lab, "jobs", id) }

func (s *Server) saveTask(t *task) error {
	return writeJSONFile(filepath.Join(s.taskDir(t.state.ID), "state.json"), t.state)
}

func (s *Server) startTask(id string, in agent.JobStart) (agent.JobState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.tasks[id]; existing != nil {
		if existing.state.VM != in.VM {
			return agent.JobState{}, errors.New("job belongs to another VM")
		}
		return existing.state, nil
	}
	if in.VM < 1 || in.VM > 16000 || in.TimeoutS < 1 || in.TimeoutS > 7*24*3600 || (in.MaxLogBytes != 0 && in.MaxLogBytes < 1024) || in.MaxLogBytes > loglimit.MaxBytes || in.Steps < 0 || in.Steps > 100000 {
		return agent.JobState{}, errors.New("invalid VM or timeout")
	}
	dir := s.taskDir(id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return agent.JobState{}, err
	}
	if in.MaxLogBytes == 0 {
		in.MaxLogBytes = loglimit.DefaultBytes
	}
	t := &task{state: agent.JobState{ID: id, VM: in.VM, Deadline: time.Now().Add(time.Duration(in.TimeoutS) * time.Second).UnixMilli(), MaxLogBytes: in.MaxLogBytes, Steps: in.Steps}, changed: make(chan struct{})}
	// Reuse a persisted identity instead of launching the same runner twice.
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		if err := json.Unmarshal(b, &t.state); err != nil {
			return t.state, err
		}
		if t.state.VM != in.VM {
			return t.state, errors.New("job belongs to another VM")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return t.state, err
	}
	if t.state.MaxLogBytes == 0 {
		t.state.MaxLogBytes = loglimit.DefaultBytes
	}
	if err := s.saveTask(t); err != nil {
		return t.state, err
	}
	s.tasks[id] = t
	if t.state.Code == nil {
		ctx, cancel := context.WithDeadline(context.Background(), time.UnixMilli(t.state.Deadline))
		t.cancel = cancel
		go s.runTask(ctx, t)
	}
	return t.state, nil
}

func (s *Server) runTask(ctx context.Context, t *task) {
	id, vm := t.state.ID, t.state.VM
	code := 1
	defer func() {
		if ctx.Err() != nil {
			code = 130
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = 124
			}
			stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res := s.execVM(stop, vm, agent.Exec{Command: `test ! -f /ci/task.pid || kill -TERM -- -$(cat /ci/task.pid)`})
			if res.Code != 0 {
				log.Printf("stop job %s: %s", id, res.Stderr)
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		t.state.Code = &code
		if err := s.saveTask(t); err != nil {
			log.Printf("persist job %s: %v", id, err)
		}
		close(t.changed)
		t.changed = make(chan struct{})
		t.cancel()
	}()
	// flock makes a repeated start after an interrupted HTTP response idempotent.
	filter := fmt.Sprintf(`{
		head -c %d
		tail -c 256 /ci/task.log >/ci/log-prefix
		dd bs=1 count=1 status=none >/ci/log-overflow
		if test -s /ci/log-overflow; then
			printf '\n[spikeloader] log truncated\n'
			cat /ci/log-prefix /ci/log-overflow - | LC_ALL=C fold -b -w 4096 | LC_ALL=C awk -v steps=%d '
		{
			line=carry $0
			carry=substr(line,length(line)>64?length(line)-63:1)
			if (match(line,/##\[(step|end|skip)\] [0-9]+( -?[0-9]+)?$/)) {
				control=substr(line,RSTART); split(control,fields," ")
				idx=fields[2]+0; rc=fields[3]+0; key=fields[1] " " idx
				if (idx<steps && rc>=-255 && rc<=255 && !seen[key]++) {
					printf "%%s %%d",fields[1],idx
					if (fields[3]!="") printf " %%d",rc
					printf "\n"
				}
			}
			fflush()
		}'
		fi
	}`, t.state.MaxLogBytes-int64(len("\n[spikeloader] log truncated\n")), t.state.Steps)
	// The guest spool is bounded too. After truncation retain at most one control marker
	// per kind/step so reattachment still reconstructs step state without retaining payload.
	runner := "echo $$ >/ci/task.pid; set -o pipefail; bash /ci/run.sh 2>&1 | " + filter + `; statuses=("${PIPESTATUS[@]}"); rc=${statuses[0]}; if [ "$rc" -eq 0 ]; then rc=${statuses[1]}; fi; echo "$rc" >/ci/task.rc`
	start := `mkdir -p /ci; (flock 9; if [ ! -f /ci/task.pid ]; then setsid bash -c ` + sshx.Quote(runner) + ` >/ci/task.log 2>&1 </dev/null 9>&- & while [ ! -f /ci/task.pid ]; do sleep .05; done; fi) 9>/ci/task.lock`
	res := s.execVM(ctx, vm, agent.Exec{Command: start})
	if res.Code != 0 {
		log.Printf("start job %s: %s", id, res.Stderr)
		return
	}
	f, err := os.OpenFile(filepath.Join(s.taskDir(id), "output"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.Printf("job output %s: %v", id, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		log.Printf("stat output %s: %v", id, err)
		return
	}
	offset := info.Size()
	writer := &taskWriter{s: s, t: t, file: f, offset: &offset}
	for ctx.Err() == nil {
		command := fmt.Sprintf(`test -f /ci/task.pid || exit 254; tail --pid="$(cat /ci/task.pid)" -c +%d -f /ci/task.log; test -f /ci/task.rc || exit 254; exit "$(cat /ci/task.rc)"`, offset+1)
		var stderr strings.Builder
		code = s.execStream(ctx, vm, command, nil, writer, &stderr)
		if writer.err != nil {
			log.Printf("write job %s: %v", id, writer.err)
			code = 1
			return
		}
		if code != 255 {
			return
		}
		// 255 may also be the runner's real exit code; do not treat it as an endless
		// transport retry once the guest has committed its result.
		result := s.execVM(ctx, vm, agent.Exec{Command: "test -f /ci/task.rc && cat /ci/task.rc"})
		if result.Code == 0 {
			exit, err := strconv.Atoi(strings.TrimSpace(string(result.Stdout)))
			if err == nil && exit >= 0 && exit <= 255 {
				code = exit
				return
			}
		}
		log.Printf("job %s SSH disconnected; reattaching at %d: %s", id, offset, stderr.String())
		pid, err := s.pid(vm)
		if err != nil || pid == nil {
			code = 1
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

type taskWriter struct {
	s      *Server
	t      *task
	file   *os.File
	offset *int64
	err    error
}

func (s *Server) copyTask(ctx context.Context, t *task, output io.Writer) (int, error) {
	path := filepath.Join(s.taskDir(t.state.ID), "output")
	offset := int64(0)
	for {
		s.mu.Lock()
		code, changed := t.state.Code, t.changed
		s.mu.Unlock()
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) && code == nil {
			select {
			case <-ctx.Done():
				return 1, ctx.Err()
			case <-changed:
				continue
			}
		}
		if err != nil {
			return 1, err
		}
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return 1, err
		}
		n, err := io.Copy(output, f)
		f.Close()
		offset += n
		if err != nil {
			return 1, err
		}
		if code != nil {
			return *code, nil
		}
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-changed:
		}
	}
}

func (w *taskWriter) Write(b []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	n, err := w.file.Write(b)
	if err == nil {
		err = w.file.Sync()
	}
	*w.offset += int64(n)
	if err != nil {
		w.err = err
	}
	close(w.t.changed)
	w.t.changed = make(chan struct{})
	return n, err
}

func (s *Server) getTask(id string) (*task, error) {
	s.mu.Lock()
	t := s.tasks[id]
	s.mu.Unlock()
	if t != nil {
		return t, nil
	}
	b, err := os.ReadFile(filepath.Join(s.taskDir(id), "state.json"))
	if err != nil {
		return nil, err
	}
	var state agent.JobState
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, err
	}
	_, err = s.startTask(id, agent.JobStart{VM: state.VM, TimeoutS: 1, MaxLogBytes: state.MaxLogBytes, Steps: state.Steps})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[id], nil
}

type frameWriter struct {
	mu     *sync.Mutex
	w      http.ResponseWriter
	stderr bool
}

func (w *frameWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	frame := agent.Chunk{Data: b}
	if w.stderr {
		frame.Data = nil
		frame.Stderr = b
	}
	if err := json.NewEncoder(w.w).Encode(frame); err != nil {
		return 0, err
	}
	http.NewResponseController(w.w).Flush()
	return len(b), nil
}

func (s *Server) streamTask(w http.ResponseWriter, r *http.Request, id string) {
	off := int64(0)
	var err error
	if raw := r.URL.Query().Get("offset"); raw != "" {
		off, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || off < 0 {
			badRequest(w, "invalid offset")
			return
		}
	}
	t, err := s.getTask(id)
	if err != nil {
		reply(w, nil, err)
		return
	}
	f, err := os.OpenFile(filepath.Join(s.taskDir(id), "output"), os.O_CREATE|os.O_RDONLY, 0600)
	if err != nil {
		reply(w, nil, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	info, err := f.Stat()
	if err != nil {
		reply(w, nil, err)
		return
	}
	if off > info.Size() {
		badRequest(w, "offset exceeds log size")
		return
	}
	controller := http.NewResponseController(w)
	controller.SetWriteDeadline(time.Time{})
	encoder := json.NewEncoder(w)
	for {
		s.mu.Lock()
		changed := t.changed
		code := t.state.Code
		s.mu.Unlock()
		buf := make([]byte, 64*1024)
		n, readErr := f.ReadAt(buf, off)
		if n > 0 {
			off += int64(n)
			if err := encoder.Encode(agent.Chunk{Data: buf[:n], Offset: off}); err != nil {
				return
			}
			controller.Flush()
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			log.Printf("read job output: %v", readErr)
			return
		}
		if code != nil {
			encoder.Encode(agent.Chunk{Offset: off, Code: code})
			controller.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		}
	}
}
