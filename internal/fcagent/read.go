// Package fcagent implements the root agent inside the outer VM.
package fcagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"spikeloader/internal/agent"
	"spikeloader/internal/env"
)

type Server struct {
	lab         string
	proc        string
	run         func(context.Context, string, ...string) ([]byte, error)
	command     func(context.Context, int, string) *exec.Cmd
	mu          sync.Mutex
	lifecycleMu sync.Mutex
	tasks       map[string]*task
	preparing   map[int]bool
}

func New(lab string) *Server {
	return &Server{lab: lab, proc: "/proc", tasks: map[string]*task{}, preparing: map[int]bool{}, run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}}
}

func readText(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

func (s *Server) hostInfo(ctx context.Context) (agent.HostInfo, error) {
	var h agent.HostInfo
	cfg, err := env.ReadFile(filepath.Join(s.lab, ".env"))
	if err != nil {
		return h, err
	}
	if h.Defaults.VCPUs, err = strconv.Atoi(cfg["VM_VCPUS"]); err != nil || h.Defaults.VCPUs < 1 {
		return h, errors.New("invalid VM_VCPUS")
	}
	if h.Defaults.MemMiB, err = strconv.Atoi(cfg["VM_MEM_MIB"]); err != nil || h.Defaults.MemMiB < 1 {
		return h, errors.New("invalid VM_MEM_MIB")
	}
	h.GuestKernel, h.GuestRootfs = cfg["GUEST_KERNEL"], cfg["GUEST_ROOTFS"]
	if h.GuestKernel == "" || h.GuestRootfs == "" {
		return h, errors.New("missing GUEST_KERNEL or GUEST_ROOTFS")
	}
	if h.Hostname, err = readText(filepath.Join(s.proc, "sys/kernel/hostname")); err != nil {
		return h, err
	}
	if h.Kernel, err = readText(filepath.Join(s.proc, "sys/kernel/osrelease")); err != nil {
		return h, err
	}
	version, err := s.run(ctx, "firecracker", "--version")
	if err != nil {
		return h, err
	}
	h.Firecracker = strings.SplitN(strings.TrimSpace(string(version)), "\n", 2)[0]
	cpus, err := s.run(ctx, "nproc")
	if err != nil {
		return h, err
	}
	if h.CPUs, err = strconv.Atoi(strings.TrimSpace(string(cpus))); err != nil || h.CPUs < 1 {
		return h, errors.New("invalid nproc output")
	}
	mem, err := readText(filepath.Join(s.proc, "meminfo"))
	if err != nil {
		return h, err
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(mem, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != "MemTotal:" && fields[0] != "MemAvailable:") {
			continue
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return h, err
		}
		values[fields[0]] = n
	}
	var total, avail bool
	h.MemTotalKB, total = values["MemTotal:"]
	h.MemAvailKB, avail = values["MemAvailable:"]
	if !total || !avail {
		return h, errors.New("missing memory statistics")
	}
	var disk syscall.Statfs_t
	if err := syscall.Statfs(s.lab, &disk); err != nil {
		return h, err
	}
	h.DiskTotalBytes = disk.Blocks * uint64(disk.Bsize)
	h.DiskAvailBytes = disk.Bavail * uint64(disk.Bsize)
	load, err := readText(filepath.Join(s.proc, "loadavg"))
	if err != nil {
		return h, err
	}
	fields := strings.Fields(load)
	if len(fields) < 3 {
		return h, errors.New("invalid loadavg")
	}
	for i := range h.Load {
		if h.Load[i], err = strconv.ParseFloat(fields[i], 64); err != nil {
			return h, err
		}
	}
	up, err := readText(filepath.Join(s.proc, "uptime"))
	if err != nil {
		return h, err
	}
	fields = strings.Fields(up)
	if len(fields) == 0 {
		return h, errors.New("invalid uptime")
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return h, err
	}
	h.UptimeS = int64(uptime)
	return h, nil
}

func allocated(info os.FileInfo) int64 {
	return info.Sys().(*syscall.Stat_t).Blocks * 512
}

func (s *Server) images() ([]agent.Image, error) {
	out := []agent.Image{}
	entries, err := os.ReadDir(filepath.Join(s.lab, "images"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".ext4") || entry.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(s.lab, "images", entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, agent.Image{
			Name: strings.TrimSuffix(entry.Name(), ".ext4"), SizeBytes: info.Size(),
			UsedBytes: allocated(info), BuiltAt: info.ModTime().Unix(),
		})
	}
	return out, nil
}

func (s *Server) vmDir(id int) string {
	return filepath.Join(s.lab, "vms", fmt.Sprintf("vm%d", id))
}

func (s *Server) pid(id int) (*int, error) {
	text, err := readText(filepath.Join(s.vmDir(id), "pid"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pid, err := strconv.Atoi(text)
	if err != nil || pid <= 0 {
		return nil, errors.New("invalid VM pid")
	}
	if err = syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &pid, nil
}

func address(id, offset int) string {
	n := uint32(172<<24|16<<16) + uint32(4*id+offset)
	return fmt.Sprintf("%d.%d.%d.%d", n>>24, n>>16&255, n>>8&255, n&255)
}

func (s *Server) vm(ctx context.Context, id int) (agent.MicroVM, error) {
	v := agent.MicroVM{
		ID: id, Name: fmt.Sprintf("vm%d", id), State: "stopped",
		IP: address(id, 2), TapIP: address(id, 1), Tap: fmt.Sprintf("fc-tap%d", id),
	}
	ip := net.ParseIP(v.IP).To4()
	v.MAC = fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", ip[0], ip[1], ip[2], ip[3])
	var config struct {
		CPU struct {
			VCPUs  int `json:"vcpu_count"`
			MemMiB int `json:"mem_size_mib"`
		} `json:"machine-config"`
		Boot struct {
			Args string `json:"boot_args"`
		} `json:"boot-source"`
	}
	b, err := os.ReadFile(filepath.Join(s.vmDir(id), "config.json"))
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(b, &config); err != nil {
		return v, err
	}
	if config.CPU.VCPUs < 1 || config.CPU.MemMiB < 1 {
		return v, errors.New("invalid VM machine-config")
	}
	v.VCPUs, v.MemMiB, v.BootArgs = config.CPU.VCPUs, config.CPU.MemMiB, config.Boot.Args
	meta, err := os.ReadFile(filepath.Join(s.vmDir(id), "meta.json"))
	if err == nil {
		var m struct {
			Owner *string `json:"owner"`
			Label *string `json:"label"`
		}
		if err := json.Unmarshal(meta, &m); err != nil {
			return v, err
		}
		v.Owner, v.Label = m.Owner, m.Label
	} else if !errors.Is(err, os.ErrNotExist) {
		return v, err
	}
	disk, err := os.Stat(filepath.Join(s.vmDir(id), "rootfs.ext4"))
	if err != nil {
		return v, err
	}
	v.DiskSizeBytes, v.DiskUsedBytes = disk.Size(), allocated(disk)
	v.PID, err = s.pid(id)
	if err != nil || v.PID == nil {
		return v, err
	}
	raw, err := s.fcGet(ctx, id, "/")
	if err != nil {
		return v, err
	}
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return v, err
	}
	switch state.State {
	case "Running", "Not started":
		v.State = "running"
	case "Paused":
		v.State = "paused"
	default:
		return v, vmmError{fmt.Errorf("invalid Firecracker state %q", state.State)}
	}
	metrics, err := s.run(ctx, "ps", "-o", "rss=,%cpu=,etimes=", "-p", strconv.Itoa(*v.PID))
	if err != nil {
		return v, err
	}
	if _, err := fmt.Sscan(string(metrics), &v.RSSKB, &v.CPUPct, &v.UptimeS); err != nil {
		return v, fmt.Errorf("invalid ps output: %w", err)
	}
	return v, nil
}

func (s *Server) vms(ctx context.Context) ([]agent.MicroVM, error) {
	out := []agent.MicroVM{}
	entries, err := os.ReadDir(filepath.Join(s.lab, "vms"))
	if err != nil {
		return nil, err
	}
	ids := []int{}
	for _, entry := range entries {
		id, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "vm"))
		if err == nil && id >= 1 && id <= 16000 && entry.Name() == fmt.Sprintf("vm%d", id) && entry.IsDir() {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	for _, id := range ids {
		v, err := s.vm(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("vm%d: %w", id, err)
		}
		out = append(out, v)
	}
	return out, nil
}

var apiPath = regexp.MustCompile(`^/[a-z/_-]*$`)

type vmmError struct{ error }

func (s *Server) fcGet(ctx context.Context, id int, path string) (raw json.RawMessage, err error) {
	defer func() {
		if err != nil {
			err = vmmError{err}
		}
	}()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(s.vmDir(id), "fc.sock"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Firecracker GET %s: HTTP %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid Firecracker response: %w", err)
	}
	return raw, nil
}
