package fcagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"spikeloader/internal/agent"
)

var imageName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
var diskSize = regexp.MustCompile(`^([1-9][0-9]*)([MG])$`)

func validImage(name string) bool {
	return imageName.MatchString(name) && !strings.Contains(name, "..")
}

func validateSpawn(in agent.Spawn) error {
	if in.VCPUs < 1 || in.VCPUs > 32 || in.MemMiB < 128 || in.MemMiB > 1048576 {
		return errors.New("invalid vcpus/mem_mib")
	}
	if !validImage(in.Image) || !diskSize.MatchString(in.Disk) || len(in.Label) > 512 {
		return errors.New("invalid image, disk or label")
	}
	return nil
}

func (s *Server) reserve() (int, error) {
	lock, err := os.OpenFile(filepath.Join(s.lab, ".ids.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return 0, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return 0, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	for id := 1; id <= 16000; id++ {
		if err := os.Mkdir(s.vmDir(id), 0700); err == nil {
			return id, nil
		} else if !errors.Is(err, os.ErrExist) {
			return 0, err
		}
	}
	return 0, errors.New("no free VM IDs")
}

func writeJSONFile(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".new", b, 0600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

func (s *Server) spawn(ctx context.Context, in agent.Spawn, retainReservation bool) (id int, err error) {
	if err = validateSpawn(in); err != nil {
		return 0, err
	}
	id, err = s.reserve()
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.preparing[id] = true
	s.mu.Unlock()
	defer func() {
		if err != nil || !retainReservation {
			s.mu.Lock()
			delete(s.preparing, id)
			s.mu.Unlock()
		}
	}()
	defer func() {
		if err != nil {
			if cleanup := s.remove(id); cleanup != nil {
				log.Printf("cleanup vm%d: %v", id, cleanup)
			}
		}
	}()
	d := s.vmDir(id)
	if err = writeJSONFile(filepath.Join(d, "meta.json"), map[string]string{"owner": "ci", "label": in.Label}); err != nil {
		return id, err
	}
	source := filepath.Join(s.lab, "images", in.Image+".ext4")
	if in.Image == "base" {
		source = filepath.Join(s.lab, "base-rootfs.ext4")
	}
	root := filepath.Join(d, "rootfs.ext4")
	if _, err = s.run(ctx, "cp", "--sparse=always", "--", source, root); err != nil {
		return id, err
	}
	if err = s.grow(ctx, root, in.Disk); err != nil {
		return id, err
	}
	tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: fmt.Sprintf("fc-tap%d", id)}, Mode: netlink.TUNTAP_MODE_TAP, Flags: netlink.TUNTAP_NO_PI}
	if err = netlink.LinkAdd(tap); err != nil {
		return id, err
	}
	addr, parseErr := netlink.ParseAddr(address(id, 1) + "/30")
	if parseErr != nil {
		return id, parseErr
	}
	if err = netlink.AddrAdd(tap, addr); err != nil {
		return id, err
	}
	if _, err = s.run(ctx, "ethtool", "-K", tap.Name, "gro", "off"); err != nil {
		return id, err
	}
	if err = netlink.LinkSetUp(tap); err != nil {
		return id, err
	}
	ip := net.ParseIP(address(id, 2)).To4()
	mac := fmt.Sprintf("06:00:%02x:%02x:%02x:%02x", ip[0], ip[1], ip[2], ip[3])
	cfg := firecracker.Config{
		SocketPath: filepath.Join(d, "fc.sock"), KernelImagePath: filepath.Join(s.lab, "vmlinux"),
		KernelArgs:        fmt.Sprintf("console=ttyS0 reboot=k panic=1 pci=off fclab.hostname=vm%d", id),
		Drives:            []models.Drive{{DriveID: firecracker.String("rootfs"), PathOnHost: firecracker.String(root), IsRootDevice: firecracker.Bool(true), IsReadOnly: firecracker.Bool(false)}},
		MachineCfg:        models.MachineConfiguration{VcpuCount: firecracker.Int64(int64(in.VCPUs)), MemSizeMib: firecracker.Int64(int64(in.MemMiB))},
		NetworkInterfaces: firecracker.NetworkInterfaces{{StaticConfiguration: &firecracker.StaticNetworkConfiguration{MacAddress: mac, HostDevName: tap.Name}}},
	}
	console, err := os.OpenFile(filepath.Join(d, "console.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return id, err
	}
	defer console.Close()
	cmd := firecracker.VMCommandBuilder{}.WithBin("firecracker").WithSocketPath(cfg.SocketPath).
		AddArgs("--log-path", filepath.Join(d, "firecracker.log"), "--level", "Info").
		WithStdout(console).WithStderr(console).Build(context.Background())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	machine, err := firecracker.NewMachine(context.Background(), cfg, firecracker.WithProcessRunner(cmd))
	if err != nil {
		return id, err
	}
	if err = machine.Start(context.Background()); err != nil {
		if stopErr := machine.StopVMM(); stopErr != nil {
			log.Printf("stop failed VM: %v", stopErr)
		}
		return id, err
	}
	if err = os.WriteFile(filepath.Join(d, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
		return id, err
	}
	raw, err := s.fcGet(ctx, id, "/vm/config")
	if err != nil {
		return id, err
	}
	if err = os.WriteFile(filepath.Join(d, "config.json"), raw, 0600); err != nil {
		return id, err
	}
	for attempt := 0; attempt < 60; attempt++ {
		result := s.execVM(ctx, id, agent.Exec{Command: "true"})
		if result.Code == 0 {
			return id, nil
		}
		select {
		case <-ctx.Done():
			return id, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return id, errors.New("microVM SSH did not become ready")
}

func (s *Server) grow(ctx context.Context, path, size string) error {
	m := diskSize.FindStringSubmatch(size)
	if m == nil {
		return errors.New("invalid disk size")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	mult := int64(1 << 20)
	if m[2] == "G" {
		mult = 1 << 30
	}
	if err != nil || n > (1<<60)/mult {
		return errors.New("disk size too large")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if n*mult <= info.Size() {
		return nil
	}
	if err := os.Truncate(path, n*mult); err != nil {
		return err
	}
	_, err = s.run(ctx, "e2fsck", "-fy", path)
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return err
	}
	_, err = s.run(ctx, "resize2fs", "-f", path)
	return err
}

func (s *Server) remove(id int) error {
	s.mu.Lock()
	var owned []*task
	for _, t := range s.tasks {
		if t.state.VM == id {
			owned = append(owned, t)
			if t.cancel != nil {
				t.cancel()
			}
		}
	}
	s.mu.Unlock()
	for _, t := range owned {
		for {
			s.mu.Lock()
			done, changed := t.state.Code != nil, t.changed
			s.mu.Unlock()
			if done {
				break
			}
			<-changed
		}
	}
	pid, err := s.pid(id)
	if err != nil {
		return err
	}
	if pid != nil {
		if err := syscall.Kill(*pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		for i := 0; i < 30; i++ {
			if syscall.Kill(*pid, 0) != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if syscall.Kill(*pid, 0) == nil {
			if err := syscall.Kill(*pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
	}
	link, err := netlink.LinkByName(fmt.Sprintf("fc-tap%d", id))
	if err == nil {
		if err := netlink.LinkDel(link); err != nil {
			return err
		}
	} else {
		var missing netlink.LinkNotFoundError
		if !errors.As(err, &missing) {
			return err
		}
	}
	if err := os.RemoveAll(s.vmDir(id)); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range owned {
		if err := os.RemoveAll(s.taskDir(t.state.ID)); err != nil {
			return err
		}
		delete(s.tasks, t.state.ID)
	}
	return nil
}

func (s *Server) sshCommand(ctx context.Context, id int, command string) *exec.Cmd {
	if s.command != nil {
		return s.command(ctx, id, command)
	}
	return exec.CommandContext(ctx, "ssh", "-i", filepath.Join(s.lab, "id_ed25519"),
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=3", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2",
		"root@"+address(id, 2), command)
}

func (s *Server) execVM(ctx context.Context, id int, in agent.Exec) agent.ExecResult {
	cmd := s.sshCommand(ctx, id, in.Command)
	cmd.Stdin = bytes.NewReader(in.Stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			stderr.WriteString(err.Error())
		}
	}
	return agent.ExecResult{Code: code, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}

// execStream keeps uploads and artifact downloads binary-safe and bounded in memory.
func (s *Server) execStream(ctx context.Context, id int, command string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := s.sshCommand(ctx, id, command)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	fmt.Fprintln(stderr, err)
	return 255
}

func (s *Server) imageBuild(ctx context.Context, in agent.ImageBuild, output io.Writer) error {
	if !validImage(in.Name) || !validImage(in.From) || !diskSize.MatchString(in.Size) {
		return errors.New("invalid image build parameters")
	}
	lock, err := os.OpenFile(filepath.Join(s.lab, "images", "."+in.Name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(s.lab, "images", in.Name+".ext4")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, err := s.spawn(ctx, agent.Spawn{VCPUs: 2, MemMiB: 2048, Image: in.From, Disk: in.Size, Label: "image build: " + in.Name}, true)
	if err != nil {
		return err
	}
	defer func() {
		if err := s.remove(id); err != nil {
			log.Printf("remove image VM: %v", err)
		}
		s.mu.Lock()
		delete(s.preparing, id)
		s.mu.Unlock()
	}()
	if code := s.execStream(ctx, id, "mkdir -p /ci; cat >/ci/run.sh", strings.NewReader("set -euo pipefail\n"+in.Script), output, output); code != 0 {
		return fmt.Errorf("image upload: exit %d", code)
	}
	// Commands are only interpreted by the microVM shell, never by the outer VM.
	taskName := fmt.Sprintf("image-vm-%d", id)
	if _, err := s.startTask(taskName, agent.JobStart{VM: id, TimeoutS: 3600}); err != nil {
		return err
	}
	t, err := s.getTask(taskName)
	if err != nil {
		return err
	}
	code, err := s.copyTask(ctx, t, output)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("image build: exit %d", code)
	}
	result := s.execVM(ctx, id, agent.Exec{Command: "rm -rf /ci /var/cache/pacman/pkg/* /tmp/*; sync"})
	if result.Code != 0 {
		return fmt.Errorf("image cleanup: exit %d", result.Code)
	}
	pid, err := s.pid(id)
	if err != nil {
		return err
	}
	if pid == nil {
		return errors.New("image VM exited before shutdown")
	}
	// Ask the guest to shut down and wait for all filesystem writes before publishing the image.
	client := firecracker.NewClient(filepath.Join(s.vmDir(id), "fc.sock"), logrus.NewEntry(logrus.StandardLogger()), false)
	if _, err := client.CreateSyncAction(ctx, &models.InstanceActionInfo{ActionType: firecracker.String("SendCtrlAltDel")}); err != nil {
		return err
	}
	// Minimal guest kernels may not deliver the virtual keyboard event to systemd.
	// Request reboot directly as well; reboot=k makes the VMM exit after unmounting root.
	reboot := s.execVM(ctx, id, agent.Exec{Command: "systemctl reboot --no-block"})
	if reboot.Code != 0 && reboot.Code != 255 {
		return fmt.Errorf("guest reboot: exit %d: %s", reboot.Code, reboot.Stderr)
	}
	for i := 0; i < 600; i++ {
		if syscall.Kill(*pid, 0) != nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if syscall.Kill(*pid, 0) == nil {
		console, err := s.run(ctx, "tail", "-n", "30", filepath.Join(s.vmDir(id), "console.log"))
		if err != nil {
			return fmt.Errorf("image VM did not shut down cleanly; console: %w", err)
		}
		return fmt.Errorf("image VM did not shut down cleanly:\n%s", console)
	}
	if err := os.Rename(filepath.Join(s.vmDir(id), "rootfs.ext4"), path+".tmp"); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
