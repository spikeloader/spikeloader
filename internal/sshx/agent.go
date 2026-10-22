package sshx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var agentForward struct {
	sync.Mutex
	dir string
}

// AgentClient uses OpenSSH stream-local forwarding through the pinned, multiplexed connection.
// The socket is private to this daemon; no TCP listener is opened.
var AgentClient = &http.Client{Transport: &http.Transport{
	DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		path, err := agentSocket(ctx)
		if err != nil {
			return nil, err
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
		if err == nil {
			return conn, nil
		}
		CloseAgent()
		path, err = agentSocket(ctx)
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	},
}}

func agentSocket(ctx context.Context) (string, error) {
	agentForward.Lock()
	defer agentForward.Unlock()
	if agentForward.dir != "" {
		return filepath.Join(agentForward.dir, "agent.sock"), nil
	}
	base, err := controlDir()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(base, "fcagent-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "agent.sock")
	if ctx.Err() != nil {
		os.RemoveAll(dir)
		return "", ctx.Err()
	}
	// A multiplex control command installs the forward on the existing master. `ssh -N`
	// as a multiplex client can exit successfully immediately, so its PID is not a liveness check.
	if result := Remote(8*time.Second, "true"); result.Code != 0 {
		os.RemoveAll(dir)
		return "", fmt.Errorf("agent SSH connection: %s", result.Stderr)
	}
	args := append(Args(), "-O", "forward", "-L", path+":/run/fcagent.sock", Target)
	result := Run(8*time.Second, "ssh", args...)
	if result.Code != 0 {
		os.RemoveAll(dir)
		return "", fmt.Errorf("agent SSH forwarding: %s", result.Stderr)
	}
	agentForward.dir = dir
	return path, nil
}

func CloseAgent() {
	agentForward.Lock()
	defer agentForward.Unlock()
	if agentForward.dir != "" {
		Run(3*time.Second, "ssh", append(Args(), "-O", "cancel", "-L", filepath.Join(agentForward.dir, "agent.sock")+":/run/fcagent.sock", Target)...)
		os.RemoveAll(agentForward.dir)
		agentForward.dir = ""
	}
}

type AgentError struct {
	Status  int
	Message string
}

func (e *AgentError) Error() string { return e.Message }

func AgentRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://fcagent"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := AgentClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var data struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, fmt.Errorf("agent HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, &AgentError{resp.StatusCode, data.Error}
	}
	return resp, nil
}

func AgentJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := AgentRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if output == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(output)
}
