package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	"spikeloader/internal/sshx"
)

var (
	upgrader = websocket.Upgrader{
		// cross-site WebSocket hijacking: the Origin must be this machine
		CheckOrigin: func(r *http.Request) bool {
			o := r.Header.Get("Origin")
			if o == "" {
				return true
			}
			u, err := url.Parse(o)
			return err == nil && localHost(u.Host)
		},
	}
	vmIDRe = regexp.MustCompile(`^\d{1,5}$`)
)

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }

// startPTY runs cmd on a new pseudo-terminal and returns its master side. The controlling terminal
// is the child's stdin (fd 0): pty.Start's own choice of Ctty is rejected by recent Go versions.
func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, err
	}
	defer tty.Close()
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(clamp(cols, 20, 500)), Rows: uint16(clamp(rows, 5, 200))}); err != nil {
		ptmx.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		ptmx.Close()
		return nil, err
	}
	return ptmx, nil
}

// terminal serves /ws/terminal: target=outer opens a shell on the outer VM; target=<id> a root
// shell on that microVM. The local PTY carries resizes (SIGWINCH) across both SSH hops.
//
// Protocol: the client sends JSON {t:"i", d:"input"} and {t:"r", c:cols, r:rows}; the server sends
// the terminal output as text frames (always valid UTF-8: the client ignores binary frames).
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	if !localHost(r.Host) {
		http.Error(w, "host not allowed", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	target := q.Get("target")
	var remote []string
	switch {
	case target == "outer":
	case vmIDRe.MatchString(target):
		remote = []string{"sudo", "fcctl", "ssh", target}
	default:
		http.Error(w, "invalid target", http.StatusBadRequest)
		return
	}
	cols, _ := strconv.Atoi(q.Get("cols"))
	rows, _ := strconv.Atoi(q.Get("rows"))
	if cols == 0 {
		cols = 100
	}
	if rows == 0 {
		rows = 30
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	args := append(sshx.Args(), "-t", sshx.Target)
	cmd := exec.Command("ssh", append(args, remote...)...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	tty, err := startPTY(cmd, cols, rows)
	if err != nil {
		_ = ws.WriteMessage(websocket.TextMessage, []byte("\r\n\x1b[31m"+err.Error()+"\x1b[0m\r\n"))
		return
	}
	defer func() {
		_ = tty.Close()
		_ = cmd.Process.Kill()
	}()

	var writeMu sync.Mutex
	send := func(b []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return ws.WriteMessage(websocket.TextMessage, b)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		var pending []byte
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				data := append(pending, buf[:n]...)
				// keep an incomplete trailing UTF-8 sequence for the next frame
				cut := len(data)
				for i := 1; i <= 3 && i <= len(data); i++ {
					c := data[len(data)-i]
					if utf8.RuneStart(c) {
						if !utf8.FullRune(data[len(data)-i:]) {
							cut = len(data) - i
						}
						break
					}
				}
				out := []byte(string([]rune(string(data[:cut])))) // invalid bytes become U+FFFD
				pending = append([]byte(nil), data[cut:]...)
				if len(out) > 0 && send(out) != nil {
					return
				}
			}
			if err != nil {
				code := "0"
				if werr := cmd.Wait(); werr != nil {
					if ee, ok := werr.(*exec.ExitError); ok {
						code = strconv.Itoa(ee.ExitCode())
					}
				}
				_ = send([]byte("\r\n\x1b[90m[session ended: " + code + "]\x1b[0m\r\n"))
				writeMu.Lock()
				_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				writeMu.Unlock()
				return
			}
		}
	}()

	go func() {
		for {
			_, raw, err := ws.ReadMessage()
			if err != nil {
				_ = tty.Close()
				return
			}
			var msg struct {
				T string `json:"t"`
				D string `json:"d"`
				C int    `json:"c"`
				R int    `json:"r"`
			}
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			switch msg.T {
			case "i":
				_, _ = tty.Write([]byte(msg.D))
			case "r":
				_ = pty.Setsize(tty, &pty.Winsize{Cols: uint16(clamp(msg.C, 20, 500)), Rows: uint16(clamp(msg.R, 5, 200))})
			}
		}
	}()
	<-done
}
