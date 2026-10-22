package fcagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"spikeloader/internal/agent"
	"spikeloader/internal/loglimit"
)

func reply(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err != nil {
		log.Printf("fcagent: %v", err)
		status := http.StatusInternalServerError
		var upstream vmmError
		if errors.As(err, &upstream) {
			status = http.StatusBadGateway
		} else if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		value = map[string]string{"error": err.Error()}
	}
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("fcagent: write response: %v", err)
	}
}

func badRequest(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		log.Printf("fcagent: write error: %v", err)
	}
}

// Handler exposes only inspection operations. Socket permissions are the trust boundary;
// callers reach it through the pinned SSH connection, never a TCP listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /host-info", func(w http.ResponseWriter, r *http.Request) {
		h, err := s.hostInfo(r.Context())
		reply(w, h, err)
	})
	list := func(w http.ResponseWriter, r *http.Request) {
		vms, err := s.vms(r.Context())
		reply(w, vms, err)
	}
	mux.HandleFunc("GET /vms", list)
	mux.HandleFunc("GET /json", list)
	mux.HandleFunc("GET /images", func(w http.ResponseWriter, r *http.Request) {
		images, err := s.images()
		reply(w, images, err)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		host, err := s.hostInfo(r.Context())
		if err != nil {
			reply(w, nil, err)
			return
		}
		vms, err := s.vms(r.Context())
		reply(w, agent.Status{Host: host, VMs: vms}, err)
	})
	withID := func(next func(http.ResponseWriter, *http.Request, int)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.Atoi(r.PathValue("id"))
			if err != nil || id < 1 || id > 16000 {
				badRequest(w, "invalid id")
				return
			}
			next(w, r, id)
		}
	}
	mux.HandleFunc("POST /vms", func(w http.ResponseWriter, r *http.Request) {
		var in agent.Spawn
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			badRequest(w, err.Error())
			return
		}
		if err := validateSpawn(in); err != nil {
			badRequest(w, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		id, err := s.spawn(ctx, in, false)
		reply(w, agent.VMID{ID: id}, err)
	})
	mux.HandleFunc("DELETE /vms/{id}", withID(func(w http.ResponseWriter, r *http.Request, id int) {
		s.lifecycleMu.Lock()
		defer s.lifecycleMu.Unlock()
		if raw := r.URL.Query().Get("pid"); raw != "" {
			s.mu.Lock()
			busy := s.preparing[id]
			for _, task := range s.tasks {
				busy = busy || (task.state.VM == id && task.state.Code == nil)
			}
			s.mu.Unlock()
			if busy {
				w.WriteHeader(http.StatusConflict)
				reply(w, map[string]string{"error": "VM is owned by an active agent operation"}, nil)
				return
			}
			expected, err := strconv.Atoi(raw)
			if err != nil || expected < 0 {
				badRequest(w, "invalid expected pid")
				return
			}
			current, err := s.pid(id)
			if err != nil {
				reply(w, nil, err)
				return
			}
			actual := 0
			if current != nil {
				actual = *current
			}
			_, configErr := os.Stat(filepath.Join(s.vmDir(id), "config.json"))
			if actual != expected || configErr != nil {
				w.WriteHeader(http.StatusConflict)
				reply(w, map[string]string{"error": "VM identity changed; refusing stale removal"}, nil)
				return
			}
		}
		reply(w, map[string]bool{"ok": true}, s.remove(id))
	}))
	mux.HandleFunc("POST /vms/{id}/exec", withID(func(w http.ResponseWriter, r *http.Request, id int) {
		http.NewResponseController(w).EnableFullDuplex()
		http.NewResponseController(w).SetReadDeadline(time.Time{})
		command := r.URL.Query().Get("command")
		if command == "" {
			badRequest(w, "missing command")
			return
		}
		if _, err := os.Stat(s.vmDir(id)); err != nil {
			reply(w, nil, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		http.NewResponseController(w).SetWriteDeadline(time.Time{})
		lock := &sync.Mutex{}
		code := s.execStream(r.Context(), id, command, r.Body,
			&frameWriter{mu: lock, w: w}, &frameWriter{mu: lock, w: w, stderr: true})
		json.NewEncoder(w).Encode(agent.Chunk{Code: &code})
	}))
	mux.HandleFunc("POST /images", func(w http.ResponseWriter, r *http.Request) {
		var in agent.ImageBuild
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&in); err != nil {
			badRequest(w, err.Error())
			return
		}
		if !validImage(in.Name) || !validImage(in.From) || !diskSize.MatchString(in.Size) {
			badRequest(w, "invalid image build parameters")
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		http.NewResponseController(w).SetWriteDeadline(time.Time{})
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		output := &frameWriter{mu: &sync.Mutex{}, w: w}
		err := s.imageBuild(ctx, in, output)
		code := 0
		if err != nil {
			code = 1
			output.Write([]byte(err.Error() + "\n"))
		}
		json.NewEncoder(w).Encode(agent.Chunk{Code: &code})
	})
	mux.HandleFunc("DELETE /images/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !validImage(name) {
			badRequest(w, "invalid image name")
			return
		}
		err := os.Remove(filepath.Join(s.lab, "images", name+".ext4"))
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		reply(w, map[string]bool{"ok": true}, err)
	})
	withTask := func(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("job")
			if !taskID.MatchString(id) {
				badRequest(w, "invalid job id")
				return
			}
			next(w, r, id)
		}
	}
	mux.HandleFunc("POST /jobs/{job}", withTask(func(w http.ResponseWriter, r *http.Request, id string) {
		var in agent.JobStart
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			badRequest(w, err.Error())
			return
		}
		if in.VM < 1 || in.VM > 16000 || in.TimeoutS < 1 || in.TimeoutS > 7*24*3600 || (in.MaxLogBytes != 0 && in.MaxLogBytes < 1024) || in.MaxLogBytes > loglimit.MaxBytes || in.Steps < 0 || in.Steps > 100000 {
			badRequest(w, "invalid VM or timeout")
			return
		}
		pid, err := s.pid(in.VM)
		if err != nil {
			reply(w, nil, err)
			return
		}
		if pid == nil {
			w.WriteHeader(http.StatusConflict)
			reply(w, map[string]string{"error": "agent no longer knows a running VM for this job"}, nil)
			return
		}
		state, err := s.startTask(id, in)
		reply(w, state, err)
	}))
	mux.HandleFunc("GET /jobs/{job}", withTask(func(w http.ResponseWriter, r *http.Request, id string) {
		t, err := s.getTask(id)
		if err != nil {
			reply(w, nil, err)
			return
		}
		s.mu.Lock()
		state := t.state
		s.mu.Unlock()
		reply(w, state, nil)
	}))
	mux.HandleFunc("DELETE /jobs/{job}", withTask(func(w http.ResponseWriter, r *http.Request, id string) {
		t, err := s.getTask(id)
		if err != nil {
			reply(w, nil, err)
			return
		}
		s.mu.Lock()
		if t.cancel != nil {
			t.cancel()
		}
		s.mu.Unlock()
		reply(w, map[string]bool{"ok": true}, nil)
	}))
	mux.HandleFunc("GET /jobs/{job}/log", withTask(s.streamTask))
	mux.HandleFunc("GET /vms/{id}/console", withID(func(w http.ResponseWriter, r *http.Request, id int) {
		lines := "+1"
		if raw := r.URL.Query().Get("lines"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 5000 {
				badRequest(w, "lines must be between 1 and 5000")
				return
			}
			lines = strconv.Itoa(n)
		}
		path := filepath.Join(s.vmDir(id), "console.log")
		if _, err := os.Stat(path); err != nil {
			reply(w, nil, err)
			return
		}
		out, err := s.run(r.Context(), "tail", "-n", lines, "--", path)
		reply(w, agent.Console{Text: string(out)}, err)
	}))
	mux.HandleFunc("GET /vms/{id}/api", withID(func(w http.ResponseWriter, r *http.Request, id int) {
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/vm/config"
		}
		if !apiPath.MatchString(path) {
			badRequest(w, "invalid path")
			return
		}
		if _, err := os.Stat(s.vmDir(id)); err != nil {
			reply(w, nil, err)
			return
		}
		pid, err := s.pid(id)
		if err != nil {
			reply(w, nil, err)
			return
		}
		if pid == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			reply(w, map[string]string{"error": fmt.Sprintf("vm%d is not running", id)}, nil)
			return
		}
		raw, err := s.fcGet(r.Context(), id, path)
		reply(w, raw, err)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || strings.HasSuffix(r.URL.Path, "/log") {
			http.NewResponseController(w).SetWriteDeadline(time.Time{})
			mux.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Listen refuses to replace regular files or active listeners, but recovers a socket left
// by an unclean shutdown. The service's primary group owns access to the socket.
func Listen(path string) (*net.UnixListener, error) {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s is not a socket", path)
		}
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("%s is already in use", path)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, dialErr
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}
