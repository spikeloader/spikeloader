package fcagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"spikeloader/internal/agent"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) *Server {
	t.Helper()
	s := New(t.TempDir())
	s.proc = filepath.Join(s.lab, "proc")
	for _, dir := range []string{"vms", "images"} {
		if err := os.Mkdir(filepath.Join(s.lab, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{
		".env":                      "VM_VCPUS=2\nVM_MEM_MIB=512\nGUEST_KERNEL=\"7.3\"\nGUEST_ROOTFS=archlinux\n",
		"proc/sys/kernel/hostname":  "spikeloader-host\n",
		"proc/sys/kernel/osrelease": "6.18-test\n",
		"proc/meminfo":              "MemTotal: 8192000 kB\nMemAvailable: 6144000 kB\n",
		"proc/loadavg":              "0.12 0.34 0.56 1/123 456\n",
		"proc/uptime":               "123.99 456.00\n",
	} {
		write(t, filepath.Join(s.lab, name), text)
	}
	run := s.run
	s.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "firecracker":
			return []byte("Firecracker v1.14.0\nextra version detail\n"), nil
		case "nproc":
			return []byte("4\n"), nil
		case "ps":
			return []byte("102400 12.5 42\n"), nil
		default:
			return run(ctx, name, args...)
		}
	}
	return s
}

func request(t *testing.T, s *Server, method, path string, code int) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
	if w.Code != code {
		t.Fatalf("%s %s: status %d, want %d; %s", method, path, w.Code, code, w.Body.String())
	}
	return w.Body.Bytes()
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var value T
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return value
}

func makeVM(t *testing.T, s *Server, id int) {
	t.Helper()
	write(t, filepath.Join(s.vmDir(id), "config.json"), `{
		"machine-config":{"vcpu_count":2,"mem_size_mib":512},
		"boot-source":{"boot_args":"console=ttyS0 reboot=k panic=1 pci=off"}
	}`)
	write(t, filepath.Join(s.vmDir(id), "rootfs.ext4"), "")
	if err := os.Truncate(filepath.Join(s.vmDir(id), "rootfs.ext4"), 16<<20); err != nil {
		t.Fatal(err)
	}
}

func fakeVMM(t *testing.T, s *Server, id int, handler http.Handler) {
	t.Helper()
	write(t, filepath.Join(s.vmDir(id), "pid"), strconv.Itoa(os.Getpid()))
	listener, err := net.Listen("unix", filepath.Join(s.vmDir(id), "fc.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
}

func TestHostAndStatus(t *testing.T) {
	s := fixture(t)
	h := decode[agent.HostInfo](t, request(t, s, "GET", "/host-info", 200))
	var disk syscall.Statfs_t
	if err := syscall.Statfs(s.lab, &disk); err != nil {
		t.Fatal(err)
	}
	want := agent.HostInfo{
		Hostname: "spikeloader-host", Kernel: "6.18-test", Firecracker: "Firecracker v1.14.0",
		CPUs: 4, MemTotalKB: 8192000, MemAvailKB: 6144000,
		DiskTotalBytes: disk.Blocks * uint64(disk.Bsize),
		Load:           [3]float64{0.12, 0.34, 0.56}, UptimeS: 123,
		GuestKernel: "7.3", GuestRootfs: "archlinux", Defaults: agent.Defaults{VCPUs: 2, MemMiB: 512},
	}
	if h.DiskAvailBytes == 0 || h.DiskAvailBytes > h.DiskTotalBytes {
		t.Fatalf("invalid free disk: %+v", h)
	}
	h.DiskAvailBytes = 0
	if !reflect.DeepEqual(h, want) {
		t.Fatalf("host = %+v, want %+v", h, want)
	}
	status := decode[agent.Status](t, request(t, s, "GET", "/status", 200))
	if status.Host.Hostname != h.Hostname || status.VMs == nil || len(status.VMs) != 0 {
		t.Fatalf("invalid status: %+v", status)
	}
	for _, endpoint := range []string{"/vms", "/json", "/images"} {
		if b := request(t, s, "GET", endpoint, 200); strings.TrimSpace(string(b)) != "[]" {
			t.Fatalf("%s = %s, want []", endpoint, b)
		}
	}
}

func TestVMContractAndOrdering(t *testing.T) {
	s := fixture(t)
	for _, id := range []int{10, 2, 16000} {
		makeVM(t, s, id)
	}
	write(t, filepath.Join(s.vmDir(10), "meta.json"), `{"owner":"ci","label":"run #1"}`)
	// A stale, nonexistent PID must remain a stopped VM, without metrics.
	write(t, filepath.Join(s.vmDir(2), "pid"), "2147483647")
	vms := decode[[]agent.MicroVM](t, request(t, s, "GET", "/json", 200))
	if len(vms) != 3 || vms[0].ID != 2 || vms[1].ID != 10 || vms[2].ID != 16000 {
		t.Fatalf("unexpected order: %+v", vms)
	}
	want := `{"id":2,"name":"vm2","state":"stopped","pid":null,"ip":"172.16.0.10","tap":"fc-tap2","tap_ip":"172.16.0.9","mac":"06:00:ac:10:00:0a","vcpus":2,"mem_mib":512,"rss_kb":0,"cpu_pct":0,"uptime_s":0,"disk_used_bytes":0,"disk_size_bytes":16777216,"boot_args":"console=ttyS0 reboot=k panic=1 pci=off","owner":null,"label":null}`
	b, err := json.Marshal(vms[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("VM JSON = %s, want %s", b, want)
	}
	if vms[1].Owner == nil || *vms[1].Owner != "ci" || vms[1].Label == nil || *vms[1].Label != "run #1" {
		t.Fatalf("metadata missing: %+v", vms[1])
	}
	if vms[2].IP != "172.16.250.2" || vms[2].MAC != "06:00:ac:10:fa:02" {
		t.Fatalf("wrong high-id address: %+v", vms[2])
	}
}

func TestRunningVMAndAPI(t *testing.T) {
	for _, state := range []string{"Running", "Paused"} {
		t.Run(state, func(t *testing.T) {
			s := fixture(t)
			makeVM(t, s, 1)
			fakeVMM(t, s, 1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("mutating Firecracker request: %s", r.Method)
				}
				switch r.URL.Path {
				case "/":
					fmt.Fprintf(w, `{"state":%q}`, state)
				case "/vm/config":
					io.WriteString(w, `{"machine-config":{"vcpu_count":2}}`)
				default:
					w.WriteHeader(400)
				}
			}))
			vms := decode[[]agent.MicroVM](t, request(t, s, "GET", "/vms", 200))
			v := vms[0]
			if v.State != strings.ToLower(state) || v.PID == nil || *v.PID != os.Getpid() ||
				v.RSSKB != 102400 || v.CPUPct != 12.5 || v.UptimeS != 42 {
				t.Fatalf("invalid running state: %+v", v)
			}
			got := strings.TrimSpace(string(request(t, s, "GET", "/vms/1/api", 200)))
			if got != `{"machine-config":{"vcpu_count":2}}` {
				t.Fatalf("unexpected API output: %s", got)
			}
			request(t, s, "GET", "/vms/1/api?path=/unsupported", 502)
		})
	}
}

func TestImagesAndConsole(t *testing.T) {
	s := fixture(t)
	path := filepath.Join(s.lab, "images", "node.ext4")
	write(t, path, strings.Repeat("x", 4096))
	mtime := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.lab, "images", "unfinished.ext4.tmp"), "ignored")
	images := decode[[]agent.Image](t, request(t, s, "GET", "/images", 200))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []agent.Image{{Name: "node", SizeBytes: 4096, UsedBytes: allocated(info), BuiltAt: mtime.Unix()}}
	if !reflect.DeepEqual(images, want) {
		t.Fatalf("images = %+v, want %+v", images, want)
	}
	write(t, filepath.Join(s.vmDir(1), "console.log"), "first\nsecond\nlast")
	for _, tc := range []struct{ query, want string }{
		{"", "first\nsecond\nlast"}, {"?lines=2", "second\nlast"}, {"?lines=1", "last"},
	} {
		got := decode[agent.Console](t, request(t, s, "GET", "/vms/1/console"+tc.query, 200))
		if got.Text != tc.want {
			t.Fatalf("console %s = %q, want %q", tc.query, got.Text, tc.want)
		}
	}
}

func TestValidationAndErrors(t *testing.T) {
	s := fixture(t)
	for _, path := range []string{
		"/vms/0/console", "/vms/16001/console", "/vms/no/console", "/vms/-1/api",
		"/vms/1/console?lines=0", "/vms/1/console?lines=-1", "/vms/1/console?lines=5001",
		"/vms/1/console?lines=oops",
	} {
		decode[map[string]string](t, request(t, s, "GET", path, 400))
	}
	for _, path := range []string{"/../config", "/machine-config?x=1", "http://other", "/UPPER"} {
		request(t, s, "GET", "/vms/1/api?path="+path, 400)
	}
	request(t, s, "POST", "/status", 405)
	request(t, s, "DELETE", "/images", 405)
	request(t, s, "POST", "/spawn", 404)
	request(t, s, "GET", "/vms/1/console", 404)
	request(t, s, "GET", "/vms/1/api", 404)
	makeVM(t, s, 1)
	request(t, s, "GET", "/vms/1/api", 409)
	write(t, filepath.Join(s.vmDir(1), "config.json"), "{invalid")
	request(t, s, "GET", "/vms", 500)
	write(t, filepath.Join(s.lab, ".env"), "VM_VCPUS=no")
	request(t, s, "GET", "/host-info", 500)
	if err := os.Remove(filepath.Join(s.lab, ".env")); err != nil {
		t.Fatal(err)
	}
	request(t, s, "GET", "/status", 404)
}

func TestDependencyErrorsAreNotSuccessfulSnapshots(t *testing.T) {
	s := fixture(t)
	s.run = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("command failed")
	}
	request(t, s, "GET", "/host-info", 500)
	makeVM(t, s, 1)
	write(t, filepath.Join(s.vmDir(1), "pid"), strconv.Itoa(os.Getpid()))
	request(t, s, "GET", "/vms", 502)
}

func TestInvalidVMState(t *testing.T) {
	s := fixture(t)
	makeVM(t, s, 1)
	fakeVMM(t, s, 1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"state":"unknown"}`)
	}))
	request(t, s, "GET", "/vms", 502)
	write(t, filepath.Join(s.vmDir(1), "config.json"), `{}`)
	request(t, s, "GET", "/vms", 500)
}

func TestUnixSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: fixture(t).Handler()}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0660 {
		t.Fatalf("socket permissions: %v, %v", info, err)
	}
	if other, err := Listen(path); err == nil {
		other.Close()
		t.Fatal("replaced active socket")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	resp, err := client.Get("http://localhost/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Unix socket HTTP status %d", resp.StatusCode)
	}
}

func TestListenRecoversOnlyStaleSockets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.sock")
	write(t, path, "must not be removed")
	if listener, err := Listen(path); err == nil {
		listener.Close()
		t.Fatal("replaced a regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "must not be removed" {
		t.Fatal("regular file changed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket not cleaned up: %v", err)
	}
}
