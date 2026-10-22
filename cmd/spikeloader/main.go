// spikeloader is the CI API client; the bash launcher retains host provisioning commands.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"spikeloader/internal/ci"
	"spikeloader/internal/env"
)

const usage = `usage: spikeloader ci <command>

Projects
  ls                                   projects and their last run
  add <name> <git-url> [--branch B] [--poll S]
  rm <project>
  hook <project>                       webhook URL for push events
  fetch <project>                      fetch the remote now (moved branches start runs)

Runs
  run <project> [branch|tag|sha] [-f]     start a pipeline (-f follows the logs)
  runs [project]                       recent runs
  show <run>                           jobs of a run
  logs <run> [job] [-f]                job log (default: every job, in order)
  cancel <run> | rerun <run>
  retry <run> <job> | approve <run> <job>

Configuration
  lint [file]                          validate a pipeline (default: ./.spikeloader.yml)
  secret ls|set|rm <project|--global> [NAME] [--unprotected|--protected]
  images                               built CI images

<job> is the job name or its index; <run> is the run number.`

type client struct {
	base, token string
	http        *http.Client
	in          io.Reader
	out, errOut io.Writer
	poll        time.Duration
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "ci" {
		args = args[1:]
	}
	token := os.Getenv("SPIKELOADER_TOKEN")
	if token == "" {
		b, err := os.ReadFile(filepath.Join(env.State, "web-token"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		token = strings.TrimSpace(string(b))
	}
	host, port := os.Getenv("SPIKELOADER_HOST"), os.Getenv("SPIKELOADER_PORT")
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "4100"
	}
	c := client{base: "http://" + net.JoinHostPort(host, port), token: token, http: &http.Client{Timeout: 2 * time.Minute}, in: os.Stdin, out: os.Stdout, errOut: os.Stderr, poll: 1500 * time.Millisecond}
	if err := c.run(args); err != nil {
		fmt.Fprintln(os.Stderr, "[spikeloader]", err)
		os.Exit(1)
	}
}

func (c *client) api(method, path string, body, out any) error {
	var input io.Reader
	if method != "GET" {
		if body == nil {
			body = struct{}{}
		}
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, input)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("panel not reachable at %s (spikeloader web start): %w", c.base, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(res.Body).Decode(&failure); err != nil {
			return fmt.Errorf("%s: invalid error response: %w", res.Status, err)
		}
		if failure.Error == "" {
			return errors.New(res.Status)
		}
		return errors.New(failure.Error)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, res.Body)
		return err
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: invalid JSON response: %w", path, err)
	}
	return nil
}

func flag(args *[]string, name string) bool {
	for i, s := range *args {
		if s == name {
			*args = append((*args)[:i], (*args)[i+1:]...)
			return true
		}
	}
	return false
}
func option(args *[]string, name, def string) (string, error) {
	for i, s := range *args {
		if s == name {
			if i+1 == len(*args) {
				return "", fmt.Errorf("%s requires a value", name)
			}
			v := (*args)[i+1]
			*args = append((*args)[:i], (*args)[i+2:]...)
			return v, nil
		}
	}
	return def, nil
}
func duration(a, b *int64) string {
	if a == nil {
		return "—"
	}
	end := time.Now().UnixMilli()
	if b != nil {
		end = *b
	}
	return fmt.Sprintf("%.0fs", float64(end-*a)/1000)
}
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
func jobIndex(r ci.Run, ref string) (int, error) {
	if n, err := strconv.Atoi(ref); err == nil && n >= 0 && n < len(r.Jobs) {
		return n, nil
	}
	for i, j := range r.Jobs {
		if j.Name == ref {
			return i, nil
		}
	}
	for i, j := range r.Jobs {
		if strings.HasPrefix(j.Name, ref) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("job %q not found in run #%d", ref, r.ID)
}

func (c *client) logs(id int64, only *int, follow bool) (ci.Run, error) {
	offsets := map[int]int64{}
	for {
		var run ci.Run
		if err := c.api("GET", fmt.Sprintf("/api/ci/runs/%d", id), nil, &run); err != nil {
			return run, err
		}
		for _, j := range run.Jobs {
			if only != nil && *only != j.Idx {
				continue
			}
			if _, seen := offsets[j.Idx]; !seen && (j.Status == "pending" || j.Status == "skipped") {
				continue
			}
			if _, seen := offsets[j.Idx]; !seen && only == nil {
				fmt.Fprintf(c.out, "\n━━ %s ━━\n", j.Name)
			}
			for {
				var tail struct {
					Text   string
					Offset int64
					Reset  bool
					Done   bool
				}
				if err := c.api("GET", fmt.Sprintf("/api/ci/runs/%d/jobs/%d/log?offset=%d", id, j.Idx, offsets[j.Idx]), nil, &tail); err != nil {
					return run, err
				}
				offsets[j.Idx] = tail.Offset
				if _, err := io.WriteString(c.out, tail.Text); err != nil {
					return run, err
				}
				if len(tail.Text) < 512*1024 {
					break
				}
			}
		}
		if !follow || (run.Status != "queued" && run.Status != "running") {
			if follow {
				fmt.Fprintf(c.out, "\nrun #%d: %s", run.ID, run.Status)
				if run.Status == "waiting" {
					fmt.Fprint(c.out, " (a manual job needs approval: spikeloader ci approve)")
				}
				fmt.Fprintln(c.out)
			}
			return run, nil
		}
		time.Sleep(c.poll)
	}
}

func (c *client) run(args []string) error {
	cmd := "help"
	if len(args) > 0 {
		cmd, args = args[0], append([]string{}, args[1:]...)
	}
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	require := func(n int) error {
		if len(args) < n {
			return fmt.Errorf("usage: spikeloader ci %s (see spikeloader ci help)", cmd)
		}
		return nil
	}
	projectPath := func() string { return "/api/ci/projects/" + url.PathEscape(arg(0)) }
	runPath := func() string { return "/api/ci/runs/" + url.PathEscape(arg(0)) }
	switch cmd {
	case "help", "--help", "-h":
		fmt.Fprintln(c.out, usage)
	case "ls":
		var v struct {
			Projects []ci.ProjectView
			Stats    ci.Stats
		}
		if err := c.api("GET", "/api/ci", nil, &v); err != nil {
			return err
		}
		if len(v.Projects) == 0 {
			fmt.Fprintln(c.out, "no projects — spikeloader ci add <name> <git-url>")
		}
		for _, p := range v.Projects {
			last := "no runs  "
			if p.LastRun != nil {
				last = fmt.Sprintf("%-9s #%d %s", p.LastRun.Status, p.LastRun.ID, p.LastRun.Branch)
			}
			fmt.Fprintf(c.out, "%-20s %s  %s", p.ID, last, p.URL)
			if p.LastError != nil {
				fmt.Fprint(c.out, "  git: "+*p.LastError)
			}
			fmt.Fprintln(c.out)
		}
		fmt.Fprintf(c.out, "\njobs: %d/%d running, %d queued", v.Stats.Running, v.Stats.MaxJobs, v.Stats.Queued)
		if !v.Stats.OuterOK {
			fmt.Fprint(c.out, " — outer VM offline")
		}
		fmt.Fprintln(c.out)
	case "add":
		branch, err := option(&args, "--branch", "main")
		if err != nil {
			return err
		}
		pollArg, err := option(&args, "--poll", "60")
		if err != nil {
			return err
		}
		poll, err := strconv.Atoi(pollArg)
		if err != nil {
			return fmt.Errorf("invalid --poll: %s", pollArg)
		}
		if err := require(2); err != nil {
			return err
		}
		var p ci.ProjectView
		if err := c.api("POST", "/api/ci/projects", ci.ProjectInput{Name: arg(0), URL: arg(1), DefaultBranch: branch, PollS: poll}, &p); err != nil {
			return err
		}
		every := strconv.Itoa(p.PollS)
		if p.PollS == 0 {
			every = "∞"
		}
		fmt.Fprintf(c.out, "project %s added — polling every %ss. Start a run with: spikeloader ci run %s\n", p.ID, every, p.ID)
	case "rm", "hook", "fetch":
		if err := require(1); err != nil {
			return err
		}
		switch cmd {
		case "rm":
			if err := c.api("DELETE", projectPath(), nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(c.out, "project %s removed\n", arg(0))
		case "hook":
			var d struct{ WebhookToken string }
			if err := c.api("GET", projectPath(), nil, &d); err != nil {
				return err
			}
			fmt.Fprintf(c.out, "%s/api/ci/hooks/%s?token=%s\n", c.base, url.PathEscape(arg(0)), url.QueryEscape(d.WebhookToken))
		case "fetch":
			var p ci.ProjectView
			if err := c.api("POST", projectPath()+"/sync", nil, &p); err != nil {
				return err
			}
			if p.LastError != nil {
				fmt.Fprintln(c.out, "git: "+*p.LastError)
			} else {
				fmt.Fprintf(c.out, "fetched %d branches\n", len(p.Refs))
			}
		}
	case "run":
		follow := flag(&args, "-f")
		follow = flag(&args, "--follow") || follow
		if err := require(1); err != nil {
			return err
		}
		var r ci.RunSummary
		if err := c.api("POST", projectPath()+"/run", map[string]string{"ref": arg(1)}, &r); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "run #%d started — %s @ %s · %s/ci/runs/%d\n", r.ID, r.Branch, short(r.Sha), c.base, r.ID)
		if follow {
			done, err := c.logs(r.ID, nil, true)
			if err != nil {
				return err
			}
			if done.Status != "success" {
				return fmt.Errorf("run #%d: %s", done.ID, done.Status)
			}
		}
	case "runs":
		path := "/api/ci/runs"
		if arg(0) != "" {
			path += "?project=" + url.QueryEscape(arg(0))
		}
		var runs []ci.RunSummary
		if err := c.api("GET", path, nil, &runs); err != nil {
			return err
		}
		if len(runs) == 0 {
			fmt.Fprintln(c.out, "no runs")
		}
		for _, r := range runs {
			message := r.Message
			if len(message) > 60 {
				message = message[:60]
			}
			fmt.Fprintf(c.out, "#%-5d %-9s %-16s %-16s %s %6s  %s\n", r.ID, r.Status, r.Project, r.Branch, short(r.Sha), duration(r.StartedAt, r.EndedAt), message)
		}
	case "show", "logs", "retry", "approve":
		if (cmd == "retry" || cmd == "approve") && len(args) < 2 {
			return fmt.Errorf("usage: spikeloader ci %s <run> <job>", cmd)
		}
		follow := flag(&args, "-f")
		follow = flag(&args, "--follow") || follow
		if err := require(1); err != nil {
			return err
		}
		var r ci.Run
		if err := c.api("GET", runPath(), nil, &r); err != nil {
			return err
		}
		switch cmd {
		case "show":
			fmt.Fprintf(c.out, "#%d %-9s %s · %s @ %s · %s · %s\n%s\n", r.ID, r.Status, r.Project, r.Branch, short(r.Sha), r.Event, duration(r.StartedAt, r.EndedAt), r.Message)
			if r.Error != nil {
				fmt.Fprintln(c.out, *r.Error)
			}
			for _, stage := range r.Stages {
				fmt.Fprintln(c.out, "\n"+stage)
				for _, j := range r.Jobs {
					if j.Stage != stage {
						continue
					}
					fmt.Fprintf(c.out, "  %2d %-9s %-28s %6s", j.Idx, j.Status, j.Name, duration(j.StartedAt, j.EndedAt))
					if j.Reason != nil {
						fmt.Fprint(c.out, "  "+*j.Reason)
					}
					if j.HasArtifacts {
						fmt.Fprint(c.out, "  [artifacts]")
					}
					fmt.Fprintln(c.out)
				}
			}
		case "logs":
			var only *int
			if arg(1) != "" {
				n, err := jobIndex(r, arg(1))
				if err != nil {
					return err
				}
				only = &n
			}
			_, err := c.logs(r.ID, only, follow)
			return err
		default:
			if err := require(2); err != nil {
				return err
			}
			n, err := jobIndex(r, arg(1))
			if err != nil {
				return err
			}
			if err := c.api("POST", runPath(), map[string]any{"action": cmd, "job": n}, nil); err != nil {
				return err
			}
			action := "approved"
			if cmd == "retry" {
				action = "retrying"
			}
			fmt.Fprintf(c.out, "%s: %s\n", r.Jobs[n].Name, action)
		}
	case "cancel", "rerun":
		if err := require(1); err != nil {
			return err
		}
		var r ci.RunSummary
		if err := c.api("POST", runPath(), map[string]string{"action": cmd}, &r); err != nil {
			return err
		}
		if cmd == "rerun" {
			fmt.Fprintf(c.out, "run #%d started\n", r.ID)
		} else {
			fmt.Fprintf(c.out, "run #%d: %s\n", r.ID, r.Status)
		}
	case "secret":
		if err := require(2); err != nil {
			return err
		}
		unprotected := flag(&args, "--unprotected")
		protected := flag(&args, "--protected")
		if unprotected && protected {
			return errors.New("choose --protected or --unprotected, not both")
		}
		scope := arg(1)
		if scope == "--global" {
			scope = "_global"
		}
		path := "/api/ci/secrets/" + url.PathEscape(scope)
		switch arg(0) {
		case "ls":
			var list []ci.SecretInfo
			if err := c.api("GET", path, nil, &list); err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(c.out, "no secrets")
			}
			for _, s := range list {
				policy := "all branches"
				if s.Protected {
					policy = "protected"
				}
				fmt.Fprintf(c.out, "%s [%s]\n", s.Name, policy)
			}
		case "set":
			if err := require(3); err != nil {
				return err
			}
			if f, ok := c.in.(*os.File); ok {
				if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
					fmt.Fprintf(c.errOut, "value for %s (end with Ctrl+D): ", arg(2))
				}
			}
			b, err := io.ReadAll(c.in)
			if err != nil {
				return err
			}
			body := map[string]any{"name": arg(2), "value": strings.TrimSuffix(string(b), "\n")}
			if unprotected || protected {
				body["protected"] = !unprotected
			}
			if err := c.api("PUT", path, body, nil); err != nil {
				return err
			}
			fmt.Fprintf(c.out, "secret %s saved\n", arg(2))
		case "rm":
			if err := require(3); err != nil {
				return err
			}
			if err := c.api("DELETE", path, map[string]string{"name": arg(2)}, nil); err != nil {
				return err
			}
			fmt.Fprintf(c.out, "secret %s removed\n", arg(2))
		default:
			return fmt.Errorf("unknown subcommand: secret %s", arg(0))
		}
	case "lint":
		file := arg(0)
		if file == "" {
			file = ".spikeloader.yml"
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		templates := map[string]string{}
		dir := filepath.Join(filepath.Dir(file), ".spikeloader", "templates")
		err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && path == dir {
				return nil
			}
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".yml" && ext != ".yaml" {
				return nil
			}
			text, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			name, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			templates[strings.TrimSuffix(filepath.ToSlash(name), ext)] = string(text)
			return nil
		})
		if err != nil {
			return err
		}
		var v struct {
			OK             bool
			Errors, Stages []string
			Jobs           []struct {
				Name, Stage, Image string
				Manual             bool
				Needs              []string
			}
		}
		if err := c.api("POST", "/api/ci/lint", map[string]any{"yaml": string(b), "templates": templates}, &v); err != nil {
			return err
		}
		if !v.OK {
			for _, err := range v.Errors {
				fmt.Fprintln(c.out, "✗ "+err)
			}
			return errors.New("pipeline validation failed")
		}
		fmt.Fprintf(c.out, "✓ %s: %d jobs in %d stages\n", file, len(v.Jobs), len(v.Stages))
		for _, stage := range v.Stages {
			fmt.Fprintln(c.out, "\n"+stage)
			for _, j := range v.Jobs {
				if j.Stage != stage {
					continue
				}
				fmt.Fprintf(c.out, "  %s · %s", j.Name, j.Image)
				if j.Manual {
					fmt.Fprint(c.out, " · manual")
				}
				if len(j.Needs) > 0 {
					fmt.Fprint(c.out, " · after "+strings.Join(j.Needs, ", "))
				}
				fmt.Fprintln(c.out)
			}
		}
	case "images":
		var v struct {
			Built       []ci.Image
			Definitions []ci.ImageDefinition
		}
		if err := c.api("GET", "/api/ci/images", nil, &v); err != nil {
			return err
		}
		fmt.Fprintln(c.out, "built")
		for _, i := range v.Built {
			fmt.Fprintf(c.out, "  %-36s %.0f MB\n", i.Name, float64(i.UsedBytes)/1048576)
		}
		if len(v.Built) == 0 {
			fmt.Fprintln(c.out, "  none yet (built on first use)")
		}
		fmt.Fprintln(c.out, "\ndefinitions (ci/images/)")
		for _, d := range v.Definitions {
			fmt.Fprintf(c.out, "  %-10s %s\n", d.Name, d.Description)
		}
	default:
		fmt.Fprintln(c.errOut, usage)
		return fmt.Errorf("unknown command: %s", cmd)
	}
	return nil
}
