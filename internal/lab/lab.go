// Package lab reports the state of the outer VM and its microVMs.
package lab

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"spikeloader/internal/env"
	"spikeloader/internal/sshx"
)

// Container is the docker-backend container running QEMU (host/common.sh).
const Container = "fclab-outer"

func readState(file string) string {
	b, err := os.ReadFile(filepath.Join(env.State, file))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Backend is how the outer VM was last started: "docker", "native" or "".
func Backend() string { return readState("backend") }

// OuterRunning reports whether the outer VM process (QEMU or its container) is up — not whether SSH
// answers yet.
func OuterRunning() bool {
	if Backend() == "docker" {
		r := sshx.Run(5*time.Second, "docker", "inspect", "-f", "{{.State.Running}}", Container)
		return strings.TrimSpace(r.Stdout) == "true"
	}
	pid, _ := strconv.Atoi(readState("qemu.pid"))
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// Outer is the outer VM part of the status.
type Outer struct {
	Running     bool    `json:"running"`
	Backend     *string `json:"backend"`
	Reachable   bool    `json:"reachable"`
	Provisioned bool    `json:"provisioned"`
}

// Status is GET /api/status (JSON shape: web/lib/types.ts LabStatus).
type Status struct {
	Version string            `json:"version"`
	Config  map[string]string `json:"config"`
	Outer   Outer             `json:"outer"`
	Host    json.RawMessage   `json:"host"` // agent host info, passed through
	VMs     json.RawMessage   `json:"vms"`  // agent microVM list, passed through
	Error   *string           `json:"error"`
}

// GetStatus asks the agent on the outer VM for the host and microVM state.
func GetStatus() Status {
	s := Status{Version: env.Version(), Config: env.Load(), Host: json.RawMessage("null"), VMs: json.RawMessage("[]")}
	if b := Backend(); b != "" {
		s.Outer.Backend = &b
	}
	_, err := os.Stat(filepath.Join(env.State, "disk.qcow2"))
	s.Outer.Provisioned = err == nil
	s.Outer.Running = OuterRunning()
	if !s.Outer.Running {
		return s
	}
	var data struct {
		Host json.RawMessage `json:"host"`
		VMs  json.RawMessage `json:"vms"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := sshx.AgentJSON(ctx, "GET", "/status", nil, &data); err != nil {
		msg := "agent: " + err.Error()
		s.Error = &msg
		return s
	}
	s.Outer.Reachable = true
	s.Host, s.VMs = data.Host, data.VMs
	if len(s.VMs) == 0 || string(s.VMs) == "null" {
		s.VMs = json.RawMessage("[]")
	}
	return s
}

// ParseID validates a microVM id.
func ParseID(raw string) (int, bool) {
	id, err := strconv.Atoi(raw)
	return id, err == nil && id >= 1 && id <= 16000
}
