// Package k8s is the read-only view of the Kubernetes cluster the pipelines deploy to, for the
// Infrastructure page. It talks to the API server directly with the kubeconfig from K8S_KUBECONFIG
// (.env): no kubectl needed. Client certificates, bearer tokens and CA data/files are supported;
// exec plugins (GKE, EKS) are not.
package k8s

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"spikeloader/internal/env"
)

/* ---------- response (JSON shape: web/lib/types.ts K8sOverview) ---------- */

// Workload is a Deployment, StatefulSet or DaemonSet.
type Workload struct {
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Ready     int      `json:"ready"`
	Desired   int      `json:"desired"`
	Updated   int      `json:"updated"`
	Images    []string `json:"images"`
	CreatedAt int64    `json:"createdAt"`
	Health    string   `json:"health"` // ok | progressing | degraded
}

// Node is one cluster node.
type Node struct {
	Name         string   `json:"name"`
	Ready        bool     `json:"ready"`
	Schedulable  bool     `json:"schedulable"`
	Roles        []string `json:"roles"`
	Version      string   `json:"version"`
	OS           string   `json:"os"`
	Runtime      string   `json:"runtime"`
	CPUMilli     float64  `json:"cpuMilli"`
	MemBytes     float64  `json:"memBytes"`
	CPUUsedMilli *float64 `json:"cpuUsedMilli"`
	MemUsedBytes *float64 `json:"memUsedBytes"`
	Pods         int      `json:"pods"`
	MaxPods      int      `json:"maxPods"`
	CreatedAt    int64    `json:"createdAt"`
}

// Cluster identifies the cluster.
type Cluster struct {
	Context  string `json:"context"`
	Server   string `json:"server"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Metrics  bool   `json:"metrics"`
}

// Namespace with workload and pod counts.
type Namespace struct {
	Name        string `json:"name"`
	Phase       string `json:"phase"`
	Workloads   int    `json:"workloads"`
	Pods        int    `json:"pods"`
	PodsRunning int    `json:"podsRunning"`
}

// PodCounts by phase.
type PodCounts struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Pending   int `json:"pending"`
	Failed    int `json:"failed"`
	Succeeded int `json:"succeeded"`
}

// Problem is a pod that is not ready, waiting or crashing.
type Problem struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	Reason    string `json:"reason"`
	Restarts  int    `json:"restarts"`
}

// Warning is a recent Warning event.
type Warning struct {
	Namespace string `json:"namespace"`
	Object    string `json:"object"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Count     int    `json:"count"`
	At        int64  `json:"at"`
}

// Overview is GET /api/k8s.
type Overview struct {
	Configured bool        `json:"configured"`
	Kubeconfig *string     `json:"kubeconfig"`
	Error      *string     `json:"error"`
	Cluster    *Cluster    `json:"cluster"`
	Nodes      []Node      `json:"nodes"`
	Namespaces []Namespace `json:"namespaces"`
	Workloads  []Workload  `json:"workloads"`
	Pods       PodCounts   `json:"pods"`
	Problems   []Problem   `json:"problems"`
	Warnings   []Warning   `json:"warnings"`
}

/* ---------- kubeconfig ---------- */

type conn struct {
	context, server, token string
	client                 *http.Client
}

type kubeconfig struct {
	CurrentContext string `yaml:"current-context"`
	Contexts       []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Clusters []struct {
		Name    string         `yaml:"name"`
		Cluster map[string]any `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string         `yaml:"name"`
		User map[string]any `yaml:"user"`
	} `yaml:"users"`
}

func kubeconfigPath() string {
	p := strings.TrimSpace(env.Load()["K8S_KUBECONFIG"])
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = home + p[1:]
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(env.Root, p)
	}
	return p
}

func connect(file string) (*conn, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var cfg kubeconfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	dir := filepath.Dir(file)
	var clusterName, userName string
	found := false
	for _, c := range cfg.Contexts {
		if c.Name == cfg.CurrentContext {
			clusterName, userName, found = c.Context.Cluster, c.Context.User, true
		}
	}
	if !found {
		return nil, fmt.Errorf("context %q not found in %s", cfg.CurrentContext, file)
	}
	var cluster, user map[string]any
	for _, c := range cfg.Clusters {
		if c.Name == clusterName {
			cluster = c.Cluster
		}
	}
	for _, u := range cfg.Users {
		if u.Name == userName {
			user = u.User
		}
	}
	server, _ := cluster["server"].(string)
	if server == "" {
		return nil, fmt.Errorf("cluster %q has no server", clusterName)
	}
	if user == nil {
		user = map[string]any{}
	}
	if user["exec"] != nil || user["auth-provider"] != nil {
		return nil, errors.New("kubeconfigs with exec/auth-provider plugins are not supported; use a token or client certificate")
	}
	// "<key>-data" is base64 inline; "<key>" is a file path relative to the kubeconfig
	pem := func(o map[string]any, key string) ([]byte, error) {
		if d, ok := o[key+"-data"].(string); ok {
			return base64.StdEncoding.DecodeString(d)
		}
		if f, ok := o[key].(string); ok {
			if !filepath.IsAbs(f) {
				f = filepath.Join(dir, f)
			}
			return os.ReadFile(f)
		}
		return nil, nil
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if skip, _ := cluster["insecure-skip-tls-verify"].(bool); skip {
		tlsCfg.InsecureSkipVerify = true
	}
	ca, err := pem(cluster, "certificate-authority")
	if err != nil {
		return nil, err
	}
	if ca != nil {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, errors.New("invalid certificate-authority in the kubeconfig")
		}
		tlsCfg.RootCAs = pool
	}
	cert, err := pem(user, "client-certificate")
	if err != nil {
		return nil, err
	}
	key, err := pem(user, "client-key")
	if err != nil {
		return nil, err
	}
	if cert != nil && key != nil {
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, err
		}
		tlsCfg.Certificates = []tls.Certificate{pair}
	}
	token, _ := user["token"].(string)
	if tf, ok := user["tokenFile"].(string); ok && token == "" {
		if !filepath.IsAbs(tf) {
			tf = filepath.Join(dir, tf)
		}
		if b, err := os.ReadFile(tf); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	return &conn{
		context: cfg.CurrentContext,
		server:  strings.TrimSuffix(server, "/"),
		token:   token,
		client:  &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg}},
	}, nil
}

func (c *conn) get(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.server+url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var status struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&status)
		if status.Message == "" {
			status.Message = resp.Status
		}
		return fmt.Errorf("%s: HTTP %d %s", url, resp.StatusCode, status.Message)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

/* ---------- quantities ---------- */

// cpuMilli reads "2", "500m", "71183n" as millicores.
func cpuMilli(q string) float64 {
	if q == "" {
		return 0
	}
	num := func(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }
	switch {
	case strings.HasSuffix(q, "n"):
		return num(q[:len(q)-1]) / 1e6
	case strings.HasSuffix(q, "u"):
		return num(q[:len(q)-1]) / 1e3
	case strings.HasSuffix(q, "m"):
		return num(q[:len(q)-1])
	}
	return num(q) * 1000
}

var units = map[string]float64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12}

// bytesOf reads "16Gi", "740Mi", "123456Ki", "1000000" as bytes.
func bytesOf(q string) float64 {
	i := strings.IndexFunc(q, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i < 0 {
		f, _ := strconv.ParseFloat(q, 64)
		return f
	}
	f, err := strconv.ParseFloat(q[:i], 64)
	if err != nil {
		return 0
	}
	if m, ok := units[q[i:]]; ok {
		return f * m
	}
	return f
}

func ts(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

/* ---------- API shapes (only the fields used here) ---------- */

type meta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	CreationTimestamp string            `json:"creationTimestamp"`
	Labels            map[string]string `json:"labels"`
}

type podSpecTemplate struct {
	Replicas *int `json:"replicas"`
	Template struct {
		Spec struct {
			Containers []struct {
				Image string `json:"image"`
			} `json:"containers"`
		} `json:"spec"`
	} `json:"template"`
}

func (s podSpecTemplate) images() []string {
	out := []string{}
	for _, c := range s.Template.Spec.Containers {
		out = append(out, c.Image)
	}
	return out
}

type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

/* ---------- overview ---------- */

var (
	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheData *Overview
	connMu    sync.Mutex
	connFile  string
	connMtime time.Time
	connVal   *conn
)

func connection(file string) (*conn, error) {
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	connMu.Lock()
	defer connMu.Unlock()
	if connVal != nil && connFile == file && connMtime.Equal(st.ModTime()) {
		return connVal, nil
	}
	c, err := connect(file)
	if err != nil {
		return nil, err
	}
	connFile, connMtime, connVal = file, st.ModTime(), c
	return c, nil
}

// Get returns the cluster snapshot, cached for a few seconds so several open tabs do not multiply
// API calls.
func Get(ctx context.Context) Overview {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cacheData != nil && time.Since(cacheAt) < 4*time.Second {
		return *cacheData
	}
	o := load(ctx)
	cacheData, cacheAt = &o, time.Now()
	return o
}

func load(ctx context.Context) Overview {
	file := kubeconfigPath()
	o := Overview{Configured: file != "", Nodes: []Node{}, Namespaces: []Namespace{}, Workloads: []Workload{}, Problems: []Problem{}, Warnings: []Warning{}}
	if file == "" {
		return o
	}
	o.Kubeconfig = &file
	c, err := connection(file)
	if err != nil {
		msg := err.Error()
		o.Error = &msg
		return o
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var (
		version struct {
			GitVersion string `json:"gitVersion"`
			Platform   string `json:"platform"`
		}
		nodes struct {
			Items []struct {
				Metadata meta `json:"metadata"`
				Spec     struct {
					Unschedulable bool `json:"unschedulable"`
				} `json:"spec"`
				Status struct {
					Allocatable map[string]string `json:"allocatable"`
					Conditions  []condition       `json:"conditions"`
					NodeInfo    struct {
						KubeletVersion          string `json:"kubeletVersion"`
						OSImage                 string `json:"osImage"`
						ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
					} `json:"nodeInfo"`
				} `json:"status"`
			} `json:"items"`
		}
		metrics struct {
			Items []struct {
				Metadata meta `json:"metadata"`
				Usage    struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
				} `json:"usage"`
			} `json:"items"`
		}
		namespaces struct {
			Items []struct {
				Metadata meta `json:"metadata"`
				Status   struct {
					Phase string `json:"phase"`
				} `json:"status"`
			} `json:"items"`
		}
		pods struct {
			Items []struct {
				Metadata meta `json:"metadata"`
				Spec     struct {
					NodeName string `json:"nodeName"`
				} `json:"spec"`
				Status struct {
					Phase             string `json:"phase"`
					Reason            string `json:"reason"`
					ContainerStatuses []struct {
						Ready        bool `json:"ready"`
						RestartCount int  `json:"restartCount"`
						State        struct {
							Waiting *struct {
								Reason string `json:"reason"`
							} `json:"waiting"`
							Terminated *struct {
								Reason string `json:"reason"`
							} `json:"terminated"`
						} `json:"state"`
					} `json:"containerStatuses"`
				} `json:"status"`
			} `json:"items"`
		}
		deploys struct {
			Items []struct {
				Metadata meta            `json:"metadata"`
				Spec     podSpecTemplate `json:"spec"`
				Status   struct {
					ReadyReplicas   int         `json:"readyReplicas"`
					UpdatedReplicas int         `json:"updatedReplicas"`
					Conditions      []condition `json:"conditions"`
				} `json:"status"`
			} `json:"items"`
		}
		sets struct {
			Items []struct {
				Metadata meta            `json:"metadata"`
				Spec     podSpecTemplate `json:"spec"`
				Status   struct {
					ReadyReplicas   int `json:"readyReplicas"`
					UpdatedReplicas int `json:"updatedReplicas"`
				} `json:"status"`
			} `json:"items"`
		}
		daemons struct {
			Items []struct {
				Metadata meta            `json:"metadata"`
				Spec     podSpecTemplate `json:"spec"`
				Status   struct {
					DesiredNumberScheduled int `json:"desiredNumberScheduled"`
					NumberReady            int `json:"numberReady"`
					UpdatedNumberScheduled int `json:"updatedNumberScheduled"`
				} `json:"status"`
			} `json:"items"`
		}
		events struct {
			Items []struct {
				Metadata       meta `json:"metadata"`
				InvolvedObject struct {
					Kind      string `json:"kind"`
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"involvedObject"`
				Reason        string `json:"reason"`
				Message       string `json:"message"`
				Count         int    `json:"count"`
				LastTimestamp string `json:"lastTimestamp"`
				EventTime     string `json:"eventTime"`
				Series        *struct {
					LastObservedTime string `json:"lastObservedTime"`
				} `json:"series"`
			} `json:"items"`
		}
	)

	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	metricsOK := true
	fetch := func(url string, out any, optional bool) {
		defer wg.Done()
		if err := c.get(ctx, url, out); err != nil {
			errMu.Lock()
			defer errMu.Unlock()
			if optional {
				if url == "/apis/metrics.k8s.io/v1beta1/nodes" {
					metricsOK = false
				}
				return
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	wg.Add(9)
	go fetch("/version", &version, false)
	go fetch("/api/v1/nodes", &nodes, false)
	go fetch("/apis/metrics.k8s.io/v1beta1/nodes", &metrics, true)
	go fetch("/api/v1/namespaces", &namespaces, false)
	go fetch("/api/v1/pods", &pods, false)
	go fetch("/apis/apps/v1/deployments", &deploys, false)
	go fetch("/apis/apps/v1/statefulsets", &sets, false)
	go fetch("/apis/apps/v1/daemonsets", &daemons, false)
	go fetch("/api/v1/events?fieldSelector=type%3DWarning&limit=500", &events, true)
	wg.Wait()

	if firstErr != nil {
		msg := firstErr.Error()
		if strings.Contains(msg, "connection refused") {
			msg = c.server + " refused the connection (is the cluster running?)"
		}
		o.Error = &msg
		o.Cluster = &Cluster{Context: c.context, Server: c.server}
		return o
	}
	o.Cluster = &Cluster{c.context, c.server, version.GitVersion, version.Platform, metricsOK}

	usage := map[string][2]float64{}
	for _, m := range metrics.Items {
		usage[m.Metadata.Name] = [2]float64{cpuMilli(m.Usage.CPU), bytesOf(m.Usage.Memory)}
	}
	podsOn := map[string]int{}
	for _, p := range pods.Items {
		if p.Spec.NodeName != "" && p.Status.Phase == "Running" {
			podsOn[p.Spec.NodeName]++
		}
	}

	for _, d := range deploys.Items {
		desired := 1
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		available, stuck := false, false
		for _, c := range d.Status.Conditions {
			available = available || (c.Type == "Available" && c.Status == "True")
			stuck = stuck || (c.Type == "Progressing" && c.Status == "False")
		}
		health := "ok"
		switch {
		case stuck || (!available && desired > 0):
			health = "degraded"
		case d.Status.ReadyReplicas < desired || d.Status.UpdatedReplicas < desired:
			health = "progressing"
		}
		o.Workloads = append(o.Workloads, Workload{"Deployment", d.Metadata.Namespace, d.Metadata.Name, d.Status.ReadyReplicas, desired, d.Status.UpdatedReplicas, d.Spec.images(), ts(d.Metadata.CreationTimestamp), health})
	}
	for _, s := range sets.Items {
		desired := 1
		if s.Spec.Replicas != nil {
			desired = *s.Spec.Replicas
		}
		health := "ok"
		if s.Status.ReadyReplicas < desired {
			health = "progressing"
		}
		o.Workloads = append(o.Workloads, Workload{"StatefulSet", s.Metadata.Namespace, s.Metadata.Name, s.Status.ReadyReplicas, desired, s.Status.UpdatedReplicas, s.Spec.images(), ts(s.Metadata.CreationTimestamp), health})
	}
	for _, d := range daemons.Items {
		health := "ok"
		if d.Status.NumberReady < d.Status.DesiredNumberScheduled {
			health = "progressing"
		}
		o.Workloads = append(o.Workloads, Workload{"DaemonSet", d.Metadata.Namespace, d.Metadata.Name, d.Status.NumberReady, d.Status.DesiredNumberScheduled, d.Status.UpdatedNumberScheduled, d.Spec.images(), ts(d.Metadata.CreationTimestamp), health})
	}
	sort.Slice(o.Workloads, func(a, b int) bool {
		if o.Workloads[a].Namespace != o.Workloads[b].Namespace {
			return o.Workloads[a].Namespace < o.Workloads[b].Namespace
		}
		return o.Workloads[a].Name < o.Workloads[b].Name
	})

	nsPods := map[string][2]int{}
	for _, p := range pods.Items {
		o.Pods.Total++
		phase := p.Status.Phase
		switch phase {
		case "Running":
			o.Pods.Running++
		case "Pending":
			o.Pods.Pending++
		case "Failed":
			o.Pods.Failed++
		case "Succeeded":
			o.Pods.Succeeded++
			continue
		}
		n := nsPods[p.Metadata.Namespace]
		n[0]++
		if phase == "Running" {
			n[1]++
		}
		nsPods[p.Metadata.Namespace] = n
		restarts, waiting, notReady := 0, "", phase != "Running"
		for _, cs := range p.Status.ContainerStatuses {
			restarts += cs.RestartCount
			notReady = notReady || !cs.Ready
			if waiting == "" && cs.State.Waiting != nil {
				waiting = cs.State.Waiting.Reason
			}
			if waiting == "" && cs.State.Terminated != nil {
				waiting = cs.State.Terminated.Reason
			}
		}
		// freshly created pods are not a problem yet
		if notReady && (waiting != "" || time.Now().UnixMilli()-ts(p.Metadata.CreationTimestamp) > 60_000) {
			reason := waiting
			if reason == "" {
				reason = p.Status.Reason
			}
			if reason == "" {
				reason = phase
				if phase == "Running" {
					reason = "NotReady"
				}
			}
			o.Problems = append(o.Problems, Problem{p.Metadata.Namespace, p.Metadata.Name, phase, reason, restarts})
		}
	}

	for _, n := range namespaces.Items {
		w := 0
		for _, x := range o.Workloads {
			if x.Namespace == n.Metadata.Name {
				w++
			}
		}
		c := nsPods[n.Metadata.Name]
		o.Namespaces = append(o.Namespaces, Namespace{n.Metadata.Name, n.Status.Phase, w, c[0], c[1]})
	}
	sort.Slice(o.Namespaces, func(a, b int) bool { return o.Namespaces[a].Name < o.Namespaces[b].Name })

	for _, ev := range events.Items {
		at := ts(ev.LastTimestamp)
		if ev.Series != nil && ev.Series.LastObservedTime != "" {
			at = ts(ev.Series.LastObservedTime)
		}
		if at == 0 {
			at = ts(ev.EventTime)
		}
		if at == 0 {
			at = ts(ev.Metadata.CreationTimestamp)
		}
		count := ev.Count
		if count == 0 {
			count = 1
		}
		o.Warnings = append(o.Warnings, Warning{ev.InvolvedObject.Namespace, ev.InvolvedObject.Kind + "/" + ev.InvolvedObject.Name, ev.Reason, ev.Message, count, at})
	}
	sort.Slice(o.Warnings, func(a, b int) bool { return o.Warnings[a].At > o.Warnings[b].At })
	if len(o.Warnings) > 12 {
		o.Warnings = o.Warnings[:12]
	}

	for _, n := range nodes.Items {
		roles := []string{}
		for l := range n.Metadata.Labels {
			if strings.HasPrefix(l, "node-role.kubernetes.io/") {
				roles = append(roles, strings.TrimPrefix(l, "node-role.kubernetes.io/"))
			}
		}
		sort.Strings(roles)
		ready := false
		for _, c := range n.Status.Conditions {
			ready = ready || (c.Type == "Ready" && c.Status == "True")
		}
		maxPods, _ := strconv.Atoi(n.Status.Allocatable["pods"])
		node := Node{
			Name: n.Metadata.Name, Ready: ready, Schedulable: !n.Spec.Unschedulable, Roles: roles,
			Version: n.Status.NodeInfo.KubeletVersion, OS: n.Status.NodeInfo.OSImage, Runtime: n.Status.NodeInfo.ContainerRuntimeVersion,
			CPUMilli: cpuMilli(n.Status.Allocatable["cpu"]), MemBytes: bytesOf(n.Status.Allocatable["memory"]),
			Pods: podsOn[n.Metadata.Name], MaxPods: maxPods, CreatedAt: ts(n.Metadata.CreationTimestamp),
		}
		if u, ok := usage[n.Metadata.Name]; ok {
			node.CPUUsedMilli, node.MemUsedBytes = &u[0], &u[1]
		}
		o.Nodes = append(o.Nodes, node)
	}
	return o
}
