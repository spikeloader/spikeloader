// Package sshx runs commands on the outer VM over SSH.
//
// The connection is multiplexed (ControlMaster) so polling stays cheap. The control socket lives in
// a directory private to this user (a predictable socket in a shared /tmp could be planted by
// another user to receive our root commands and secrets), and the VM's host key is pinned on first
// use in .lab/known_hosts (reset when its disk is recreated).
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"spikeloader/internal/env"
)

// Target is the outer VM account the panel uses (passwordless sudo).
const Target = "lab@127.0.0.1"

func controlDir() (string, error) {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d, nil
		}
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("spikeloader-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || !ok || int(sys.Uid) != os.Getuid() || st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s must be a directory owned by you with mode 0700", dir)
	}
	return dir, nil
}

// Args are the ssh options shared by every connection to the outer VM.
func Args() []string {
	ctl, err := controlDir()
	if err != nil {
		ctl = os.TempDir() // ssh then refuses to multiplex rather than failing outright
	}
	return []string{
		"-i", filepath.Join(env.State, "id_ed25519"),
		"-p", env.Get("SSH_PORT", "2222"),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + filepath.Join(env.State, "known_hosts"),
		"-o", "HostKeyAlias=spikeloader-outer",
		"-o", "LogLevel=ERROR",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=2",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(ctl, "spikeloader-ssh-%C"),
		"-o", "ControlPersist=120",
	}
}

// Quote quotes one argument for the remote shell (ssh joins its arguments into one command line).
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Command joins arguments into a remote command line, each passed literally.
func Command(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}

// Result of a finished command.
type Result struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// Run executes a local command with a timeout and collects its output.
func Run(timeout time.Duration, name string, args ...string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := Result{Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.Code = ee.ExitCode()
		if r.Code < 0 {
			r.Code = 1
		}
	default:
		r.Code = 1
		if r.Stderr == "" {
			r.Stderr = err.Error()
		}
	}
	if ctx.Err() == context.DeadlineExceeded && r.Stderr == "" {
		r.Stderr = "timed out"
	}
	return r
}

// Remote runs a command on the outer VM; each argument is passed literally.
func Remote(timeout time.Duration, args ...string) Result {
	return Run(timeout, "ssh", append(Args(), Target, "--", Command(args...))...)
}

// CloseMaster ends the multiplexed connection (before the outer VM shuts down).
func CloseMaster() {
	Run(3*time.Second, "ssh", append(Args(), "-O", "exit", Target)...)
}

// Stream options for an agent command.
type Stream struct {
	Stdin   string    // file streamed to the remote stdin
	Stdout  io.Writer // remote stdout (default: OnData)
	OnData  func([]byte)
	Timeout time.Duration
}

func emit(f func([]byte), s string) {
	if f != nil {
		f([]byte(s))
	}
}
