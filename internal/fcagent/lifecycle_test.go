package fcagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spikeloader/internal/agent"
)

func TestLifecycleValidation(t *testing.T) {
	valid := agent.Spawn{VCPUs: 1, MemMiB: 256, Image: "base", Disk: "10G"}
	if err := validateSpawn(valid); err != nil {
		t.Fatal(err)
	}
	for _, in := range []agent.Spawn{
		{VCPUs: 0, MemMiB: 256, Image: "base", Disk: "10G"},
		{VCPUs: 1, MemMiB: 0, Image: "base", Disk: "10G"},
		{VCPUs: 1, MemMiB: 256, Image: "../base", Disk: "10G"},
		{VCPUs: 1, MemMiB: 256, Image: "base", Disk: "-1G"},
	} {
		if err := validateSpawn(in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
	s := fixture(t)
	for _, path := range []string{"/vms", "/images"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if w.Code != 400 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	request(t, s, "DELETE", "/images/..bad", 400)
	write(t, filepath.Join(s.lab, "images", "node.ext4"), "image")
	request(t, s, "DELETE", "/images/node", 200)
	request(t, s, "DELETE", "/images/node", 200)
	makeVM(t, s, 1)
	write(t, filepath.Join(s.vmDir(1), "pid"), "2147483647")
	request(t, s, "DELETE", "/vms/1?pid=12345", 409)
	request(t, s, "DELETE", "/vms/1?pid=bad", 400)
	request(t, s, "POST", "/vms/1/exec", 400)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/jobs/missing", strings.NewReader(`{"vm":1,"timeoutS":10}`)))
	if w.Code != 409 {
		t.Fatalf("missing VM start = %d: %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(s.lab, "images", "node.ext4")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestParallelIDReservation(t *testing.T) {
	s := fixture(t)
	var wg sync.WaitGroup
	ids := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.reserve()
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[int]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate VM %d", id)
		}
		seen[id] = true
	}
	if len(seen) != 20 {
		t.Fatalf("reserved %d IDs", len(seen))
	}
}

func TestReaperPreservesAgentOwnedVMs(t *testing.T) {
	for _, preparing := range []bool{true, false} {
		s := fixture(t)
		s.preparing[1] = preparing
		if !preparing {
			s.tasks["active"] = &task{state: agent.JobState{VM: 1}}
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("DELETE", "/vms/1?pid=0", nil))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "active agent operation") {
			t.Fatalf("preparing=%v: status=%d body=%s", preparing, w.Code, w.Body)
		}
	}
}

func localTaskServer(t *testing.T, script string) *Server {
	t.Helper()
	s := fixture(t)
	ci := filepath.Join(s.lab, "ci")
	write(t, filepath.Join(ci, "run.sh"), script)
	s.command = func(ctx context.Context, _ int, command string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "bash", "-c", strings.ReplaceAll(command, "/ci", ci))
		cmd.WaitDelay = time.Second
		return cmd
	}
	return s
}

func readFrames(t *testing.T, body io.Reader) (string, int) {
	t.Helper()
	var out strings.Builder
	decoder := json.NewDecoder(body)
	for {
		var frame agent.Chunk
		if err := decoder.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		out.Write(frame.Data)
		if frame.Code != nil {
			return out.String(), *frame.Code
		}
	}
}

func TestTaskStreamsUnterminatedOutput(t *testing.T) {
	s := localTaskServer(t, "printf PROMPT; sleep 2; echo DONE\n")
	if _, err := s.startTask("prompt", agent.JobStart{VM: 1, TimeoutS: 10}); err != nil {
		t.Fatal(err)
	}
	task, err := s.getTask("prompt")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		b, err := os.ReadFile(filepath.Join(s.taskDir("prompt"), "output"))
		if err == nil && string(b) == "PROMPT" {
			break
		}
		if i == 99 {
			t.Fatal("unterminated output did not stream before the runner completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out strings.Builder
	if rc, err := s.copyTask(ctx, task, &out); err != nil || rc != 0 {
		t.Fatalf("%d %v", rc, err)
	}
	if out.String() != "PROMPTDONE\n" {
		t.Fatal(out.String())
	}
}

func TestTaskSpoolLimitRetainsControl(t *testing.T) {
	s := localTaskServer(t, `echo '##[step] 0'
head -c 3000000 /dev/zero | tr '\0' x
echo
echo '##[end] 0 0'
echo '##[step] 1'
echo AFTER_LIMIT
echo '##[end] 1 7'
exit 7
`)
	if _, err := s.startTask("limited", agent.JobStart{VM: 1, TimeoutS: 10, MaxLogBytes: 1 << 20, Steps: 2}); err != nil {
		t.Fatal(err)
	}
	task, err := s.getTask("limited")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rc, err := s.copyTask(ctx, task, &out)
	if err != nil || rc != 7 {
		t.Fatalf("rc=%d err=%v", rc, err)
	}
	if out.Len() > 1<<20+256 || strings.Count(out.String(), "log truncated") != 1 {
		t.Fatalf("size=%d", out.Len())
	}
	if !strings.Contains(out.String(), "##[end] 0 0") || !strings.Contains(out.String(), "##[end] 1 7") || strings.Contains(out.String(), "AFTER_LIMIT") {
		t.Fatal("control or truncation lost")
	}
	guest, err := os.Stat(filepath.Join(s.lab, "ci", "task.log"))
	if err != nil {
		t.Fatal(err)
	}
	if guest.Size() > 1<<20+256 {
		t.Fatal("guest spool is unbounded")
	}
}

func TestOwnedTaskStreamingAndReplay(t *testing.T) {
	s := localTaskServer(t, "echo BEGIN; sleep .2; echo END\n")
	state, err := s.startTask("run-1", agent.JobStart{VM: 1, TimeoutS: 10})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.startTask("run-1", agent.JobStart{VM: 1, TimeoutS: 20})
	if err != nil || second.Deadline != state.Deadline {
		t.Fatalf("non-idempotent start: %+v %v", second, err)
	}
	if _, err := s.startTask("run-1", agent.JobStart{VM: 2, TimeoutS: 20}); err == nil {
		t.Fatal("rebound job to another VM")
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(server.URL + "/jobs/run-1/log")
	if err != nil {
		t.Fatal(err)
	}
	out, code := readFrames(t, resp.Body)
	resp.Body.Close()
	if out != "BEGIN\nEND\n" || code != 0 {
		t.Fatalf("output=%q exit=%d", out, code)
	}
	resp, err = client.Get(server.URL + "/jobs/run-1/log?offset=6")
	if err != nil {
		t.Fatal(err)
	}
	out, code = readFrames(t, resp.Body)
	resp.Body.Close()
	if out != "END\n" || code != 0 {
		t.Fatalf("replay=%q exit=%d", out, code)
	}
	// Persisted completed jobs remain queryable after the agent process restarts.
	restarted := New(s.lab)
	recovered, err := restarted.getTask("run-1")
	if err != nil || recovered.state.Code == nil || *recovered.state.Code != 0 {
		t.Fatalf("recovery: %+v, %v", recovered, err)
	}
}

func TestOwnedTaskTimeoutAndCancel(t *testing.T) {
	for _, cancelJob := range []bool{false, true} {
		t.Run(map[bool]string{true: "cancel", false: "timeout"}[cancelJob], func(t *testing.T) {
			s := localTaskServer(t, "echo BEGIN; sleep 60; echo SHOULD_NOT_RUN\n")
			timeout := 1
			if cancelJob {
				timeout = 60
			}

			_, err := s.startTask("job", agent.JobStart{VM: 1, TimeoutS: timeout})
			if err != nil {
				t.Fatal(err)
			}
			if cancelJob {
				time.Sleep(200 * time.Millisecond)
				request(t, s, "DELETE", "/jobs/job", 200)
			}
			server := httptest.NewServer(s.Handler())
			defer server.Close()
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Get(server.URL + "/jobs/job/log")
			if err != nil {
				t.Fatal(err)
			}
			out, code := readFrames(t, resp.Body)
			resp.Body.Close()
			want := 124
			if cancelJob {
				want = 130
			}
			if code != want || strings.Contains(out, "SHOULD_NOT_RUN") {
				t.Fatalf("exit %d output %q", code, out)
			}
		})
	}
}

func TestOwnedTaskExit255IsNotATransportRetry(t *testing.T) {
	s := localTaskServer(t, "echo expected-failure; exit 255\n")
	if _, err := s.startTask("exit255", agent.JobStart{VM: 1, TimeoutS: 10}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(server.URL + "/jobs/exit255/log")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, code := readFrames(t, resp.Body)
	if code != 255 || out != "expected-failure\n" {
		t.Fatalf("exit %d output %q", code, out)
	}
}
