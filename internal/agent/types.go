// Package agent defines the HTTP/JSON contract of the outer VM agent.
// HostInfo and MicroVM mirror web/lib/types.ts and the existing fcctl output.
package agent

type Defaults struct {
	VCPUs  int `json:"vcpus"`
	MemMiB int `json:"mem_mib"`
}

type HostInfo struct {
	Hostname       string     `json:"hostname"`
	Kernel         string     `json:"kernel"`
	Firecracker    string     `json:"firecracker"`
	CPUs           int        `json:"cpus"`
	MemTotalKB     uint64     `json:"mem_total_kb"`
	MemAvailKB     uint64     `json:"mem_avail_kb"`
	DiskTotalBytes uint64     `json:"disk_total_bytes"`
	DiskAvailBytes uint64     `json:"disk_avail_bytes"`
	Load           [3]float64 `json:"load"`
	UptimeS        int64      `json:"uptime_s"`
	GuestKernel    string     `json:"guest_kernel"`
	GuestRootfs    string     `json:"guest_rootfs"`
	Defaults       Defaults   `json:"defaults"`
}

type MicroVM struct {
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	State         string  `json:"state"`
	PID           *int    `json:"pid"`
	IP            string  `json:"ip"`
	Tap           string  `json:"tap"`
	TapIP         string  `json:"tap_ip"`
	MAC           string  `json:"mac"`
	VCPUs         int     `json:"vcpus"`
	MemMiB        int     `json:"mem_mib"`
	RSSKB         int64   `json:"rss_kb"`
	CPUPct        float64 `json:"cpu_pct"`
	UptimeS       int64   `json:"uptime_s"`
	DiskUsedBytes int64   `json:"disk_used_bytes"`
	DiskSizeBytes int64   `json:"disk_size_bytes"`
	BootArgs      string  `json:"boot_args"`
	Owner         *string `json:"owner"`
	Label         *string `json:"label"`
}

type Status struct {
	Host HostInfo  `json:"host"`
	VMs  []MicroVM `json:"vms"`
}

type Image struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	UsedBytes int64  `json:"used_bytes"`
	BuiltAt   int64  `json:"built_at"`
}

type Console struct {
	Text string `json:"text"`
}

type Spawn struct {
	VCPUs  int    `json:"vcpus"`
	MemMiB int    `json:"mem_mib"`
	Image  string `json:"image"`
	Disk   string `json:"disk"`
	Label  string `json:"label"`
}

type VMID struct {
	ID int `json:"id"`
}

type Exec struct {
	Command string `json:"command"`
	Stdin   []byte `json:"stdin,omitempty"`
}

type ExecResult struct {
	Code   int    `json:"code"`
	Stdout []byte `json:"stdout"`
	Stderr []byte `json:"stderr"`
}

type ImageBuild struct {
	Name   string `json:"name"`
	From   string `json:"from"`
	Size   string `json:"size"`
	Script string `json:"script"`
}

// Chunk frames binary exec data or replayable job log bytes; Code terminates the stream.
type Chunk struct {
	Data   []byte `json:"data,omitempty"`
	Stderr []byte `json:"stderr,omitempty"`
	Offset int64  `json:"offset"`
	Code   *int   `json:"code,omitempty"`
}

type JobStart struct {
	VM          int   `json:"vm"`
	TimeoutS    int   `json:"timeoutS"`
	MaxLogBytes int64 `json:"maxLogBytes"`
	Steps       int   `json:"steps"`
}

type JobState struct {
	ID          string `json:"id"`
	VM          int    `json:"vm"`
	Deadline    int64  `json:"deadline"`
	Code        *int   `json:"code"`
	MaxLogBytes int64  `json:"maxLogBytes"`
	Steps       int    `json:"steps"`
}
