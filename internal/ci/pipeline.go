package ci

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parser for .spikeloader.yml. Every job runs in its own microVM; templates (`uses:`) come from the
// repository's .spikeloader/templates/ or from the built-in ci/templates/.

// PipelineFiles are the file names looked up at the repository root, in order.
var PipelineFiles = []string{".spikeloader.yml", ".spikeloader.yaml"}

// DefaultStages apply when the pipeline declares none.
var DefaultStages = []string{"build", "test", "scan", "deploy"}

var (
	jobKeys      = set("stage", "image", "uses", "with", "vcpus", "mem", "disk", "timeout", "needs", "matrix", "variables", "secrets", "before", "steps", "after", "artifacts", "cache", "when", "manual", "allow_failure", "retry", "depth")
	templateKeys = union(jobKeys, set("inputs"))
	topKeys      = set("version", "on", "variables", "defaults", "stages", "jobs")
	defaultKeys  = set("image", "vcpus", "mem", "disk", "timeout", "retry", "depth", "before", "after")

	nameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	jobNameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]*$`)
	imageRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	templateRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*$`)
	diskRe     = regexp.MustCompile(`^\d+[MG]$`)
	durationRe = regexp.MustCompile(`^(\d+)\s*(s|m|h)?$`)
)

// TemplateReader returns a template's YAML by name, or ok=false when it does not exist.
type TemplateReader func(name string) (text string, ok bool)

/* ---------- ordered YAML values ---------- */

// obj is a YAML map that keeps its key order (jobs run in the order they are written).
type obj struct {
	keys []string
	m    map[string]any
}

func (o *obj) get(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.m[k]
	return v, ok
}

// value converts a YAML node into nil, bool, int64, float64, string, []any or *obj. Timestamps and
// other tags stay strings, as in the YAML 1.2 core schema the TypeScript version used.
func value(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return value(n.Content[0])
	case yaml.AliasNode:
		return value(n.Alias)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := value(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		o := &obj{m: map[string]any{}}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, err := value(n.Content[i])
			if err != nil {
				return nil, err
			}
			key := jsString(k)
			v, err := value(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			if _, dup := o.m[key]; dup {
				return nil, fmt.Errorf("line %d: duplicate key %q", n.Content[i].Line, key)
			}
			o.keys = append(o.keys, key)
			o.m[key] = v
		}
		return o, nil
	default:
		switch n.Tag {
		case "!!null":
			return nil, nil
		case "!!bool":
			var b bool
			if err := n.Decode(&b); err != nil {
				return nil, err
			}
			return b, nil
		case "!!int":
			var i int64
			if err := n.Decode(&i); err != nil {
				var f float64
				if ferr := n.Decode(&f); ferr != nil {
					return nil, err
				}
				return f, nil
			}
			return i, nil
		case "!!float":
			var f float64
			if err := n.Decode(&f); err != nil {
				return nil, err
			}
			return f, nil
		default:
			return n.Value, nil
		}
	}
}

func parseYAML(text string) (any, error) {
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(text), &n); err != nil {
		return nil, err
	}
	return value(&n)
}

/* ---------- JavaScript-like conversions (the pipeline semantics come from the TypeScript engine) ---------- */

func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return fmt.Sprint(x)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = jsString(e)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

func jsNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case int64:
		return float64(x)
	case float64:
		return x
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return math.NaN()
		}
		return f
	default:
		return math.NaN()
	}
}

func isInteger(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) }

/* ---------- helpers ---------- */

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func union(a, b map[string]bool) map[string]bool {
	m := make(map[string]bool, len(a)+len(b))
	for k := range a {
		m[k] = true
	}
	for k := range b {
		m[k] = true
	}
	return m
}

// ParseDuration reads "90", "90s", "5m" or "2h" as seconds.
func ParseDuration(v any, fallback int) (int, error) {
	if v == nil {
		return fallback, nil
	}
	switch x := v.(type) {
	case int64:
		return int(x), nil
	case float64:
		return int(x), nil
	}
	m := durationRe.FindStringSubmatch(strings.TrimSpace(jsString(v)))
	if m == nil {
		return 0, fmt.Errorf("invalid duration: %s", jsString(v))
	}
	n, _ := strconv.Atoi(m[1])
	return n * map[string]int{"": 1, "s": 1, "m": 60, "h": 3600}[m[2]], nil
}

// GlobMatch matches branch names: `*` stays within a path segment, `**` crosses them.
func GlobMatch(pattern, value string) bool {
	parts := strings.Split(pattern, "**")
	for i, p := range parts {
		segs := strings.Split(p, "*")
		for j, s := range segs {
			segs[j] = regexp.QuoteMeta(s)
		}
		parts[i] = strings.Join(segs, "[^/]*")
	}
	re, err := regexp.Compile("^" + strings.Join(parts, ".*") + "$")
	return err == nil && re.MatchString(value)
}

func strList(v any, what string) ([]string, error) {
	switch x := v.(type) {
	case nil:
		return []string{}, nil
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			switch e.(type) {
			case string, int64, float64:
				out = append(out, jsString(e))
			default:
				return nil, fmt.Errorf("%s must be a string or a list of strings", what)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s must be a string or a list of strings", what)
}

func strMap(v any, what string) (map[string]string, error) {
	out := map[string]string{}
	if v == nil {
		return out, nil
	}
	o, ok := v.(*obj)
	if !ok {
		return nil, fmt.Errorf("%s must be a map", what)
	}
	for _, k := range o.keys {
		if !nameRe.MatchString(k) {
			return nil, fmt.Errorf("%s: invalid variable name %q", what, k)
		}
		if o.m[k] == nil {
			out[k] = ""
		} else {
			out[k] = jsString(o.m[k])
		}
	}
	return out, nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	if r := []rune(l); len(r) > 80 {
		return string(r[:80])
	}
	return l
}

func parseSteps(v any, what string) ([]StepSpec, error) {
	if v == nil {
		return []StepSpec{}, nil
	}
	list, ok := v.([]any)
	if s, isStr := v.(string); isStr {
		list, ok = []any{s}, true
	}
	if !ok {
		return nil, fmt.Errorf("%s must be a list of commands", what)
	}
	out := make([]StepSpec, 0, len(list))
	for i, s := range list {
		switch x := s.(type) {
		case string:
			out = append(out, StepSpec{Name: firstLine(x), Run: x})
		case *obj:
			run, isStr := x.m["run"].(string)
			if !isStr {
				return nil, fmt.Errorf("%s[%d] must be a command or { name, run }", what, i)
			}
			name, isName := x.m["name"].(string)
			if !isName {
				name = firstLine(run)
			}
			out = append(out, StepSpec{Name: name, Run: run})
		default:
			return nil, fmt.Errorf("%s[%d] must be a command or { name, run }", what, i)
		}
	}
	return out, nil
}

func parseCaches(v any, job string) ([]CacheSpec, error) {
	if v == nil {
		return []CacheSpec{}, nil
	}
	var list []any
	if arr, ok := v.([]any); ok {
		allStr := true
		for _, e := range arr {
			if _, ok := e.(string); !ok {
				allStr = false
			}
		}
		if allStr {
			list = []any{&obj{keys: []string{"paths"}, m: map[string]any{"paths": arr}}}
		} else {
			list = arr
		}
	} else {
		list = []any{v}
	}
	out := make([]CacheSpec, 0, len(list))
	for i, c := range list {
		if s, ok := c.(string); ok {
			out = append(out, CacheSpec{Key: fmt.Sprintf("%s-%d", job, i), Paths: []string{s}, Files: []string{}})
			continue
		}
		o, ok := c.(*obj)
		if !ok {
			return nil, errors.New("cache must be a list of paths or { key, paths, files }")
		}
		paths, err := strList(o.m["paths"], "cache.paths")
		if err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			return nil, errors.New("cache.paths is required")
		}
		files, err := strList(o.m["files"], "cache.files")
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s-%d", job, i)
		if k, has := o.get("key"); has {
			key = jsString(k)
		}
		out = append(out, CacheSpec{Key: key, Paths: paths, Files: files})
	}
	return out, nil
}

func parseArtifacts(v any) (*ArtifactSpec, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case []any, string:
		paths, err := strList(x, "artifacts")
		if err != nil {
			return nil, err
		}
		return &ArtifactSpec{Paths: paths, When: "success"}, nil
	case *obj:
		when := "success"
		if w, ok := x.m["when"]; ok && w != nil {
			when = jsString(w)
		}
		if !slices.Contains([]string{"success", "failure", "always"}, when) {
			return nil, errors.New("artifacts.when must be success, failure or always")
		}
		paths, err := strList(x.m["paths"], "artifacts.paths")
		if err != nil {
			return nil, err
		}
		return &ArtifactSpec{Paths: paths, When: when}, nil
	}
	return nil, errors.New("artifacts must be a list of paths or { paths, when }")
}

type matrixAxis struct {
	key    string
	values []string
}

func cartesian(axes []matrixAxis) []map[string]string {
	combos := []map[string]string{{}}
	for _, a := range axes {
		var next []map[string]string
		for _, c := range combos {
			for _, v := range a.values {
				m := make(map[string]string, len(c)+1)
				for k, x := range c {
					m[k] = x
				}
				m[a.key] = v
				next = append(next, m)
			}
		}
		combos = next
	}
	return combos
}

// applyTemplate resolves `uses:` into a merged raw job (template first, job keys win).
func applyTemplate(raw *obj, read TemplateReader) (*obj, error) {
	usesV, has := raw.get("uses")
	if !has {
		return raw, nil
	}
	ref := jsString(usesV)
	if !templateRe.MatchString(ref) || strings.Contains(ref, "..") {
		return nil, fmt.Errorf("uses: invalid template name %q", ref)
	}
	text, ok := read(ref)
	if !ok {
		return nil, fmt.Errorf("uses: template %q not found", ref)
	}
	parsed, err := parseYAML(text)
	if err != nil {
		return nil, fmt.Errorf("template %q: %v", ref, err)
	}
	tpl, isObj := parsed.(*obj)
	if !isObj {
		return nil, fmt.Errorf("template %q is not a map", ref)
	}
	for _, k := range tpl.keys {
		if !templateKeys[k] {
			return nil, fmt.Errorf("template %q: unknown key %q", ref, k)
		}
	}
	inputsV, _ := tpl.get("inputs")
	inputs, err := strMap(inputsV, fmt.Sprintf("template %q inputs", ref))
	if err != nil {
		return nil, err
	}
	var inputOrder []string
	if o, ok := inputsV.(*obj); ok {
		inputOrder = o.keys
	}
	given := &obj{m: map[string]any{}}
	if w, has := raw.get("with"); has {
		o, isMap := w.(*obj)
		if !isMap {
			return nil, errors.New("with must be a map")
		}
		given = o
	}
	vars := &obj{m: map[string]any{}}
	put := func(k string, v any) {
		if _, ok := vars.m[k]; !ok {
			vars.keys = append(vars.keys, k)
		}
		vars.m[k] = v
	}
	for _, k := range inputOrder {
		put("INPUT_"+strings.ToUpper(k), inputs[k])
	}
	for _, k := range given.keys {
		if _, ok := inputs[k]; !ok {
			names := strings.Join(inputOrder, ", ")
			if names == "" {
				names = "none"
			}
			return nil, fmt.Errorf("with: template %q has no input %q (inputs: %s)", ref, k, names)
		}
		put("INPUT_"+strings.ToUpper(k), jsString(given.m[k]))
	}
	for _, src := range []*obj{tpl, raw} {
		if v, ok := src.get("variables"); ok {
			if o, isMap := v.(*obj); isMap {
				for _, k := range o.keys {
					put(k, o.m[k])
				}
			}
		}
	}

	merged := &obj{m: map[string]any{}}
	for _, src := range []*obj{tpl, raw} {
		for _, k := range src.keys {
			if _, ok := merged.m[k]; !ok {
				merged.keys = append(merged.keys, k)
			}
			merged.m[k] = src.m[k]
		}
	}
	delete(merged.m, "inputs")
	merged.keys = slices.DeleteFunc(merged.keys, func(k string) bool { return k == "inputs" })
	if _, ok := merged.m["variables"]; !ok {
		merged.keys = append(merged.keys, "variables")
	}
	merged.m["variables"] = vars

	tplSecrets, err := strList(tpl.m["secrets"], "secrets")
	if err != nil {
		return nil, err
	}
	rawSecrets, err := strList(raw.m["secrets"], "secrets")
	if err != nil {
		return nil, err
	}
	var secrets []any
	seen := map[string]bool{}
	for _, s := range append(tplSecrets, rawSecrets...) {
		if !seen[s] {
			seen[s] = true
			secrets = append(secrets, s)
		}
	}
	if _, ok := merged.m["secrets"]; !ok {
		merged.keys = append(merged.keys, "secrets")
	}
	merged.m["secrets"] = append([]any{}, secrets...)
	return merged, nil
}

// ParsePipeline parses and validates a pipeline. On error the pipeline is nil and errors lists
// every problem found.
func ParsePipeline(text string, readTemplate TemplateReader) (*Pipeline, []string) {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	parsed, err := parseYAML(text)
	if err != nil {
		return nil, []string{"YAML: " + err.Error()}
	}
	doc, ok := parsed.(*obj)
	if !ok {
		return nil, []string{"the pipeline must be a YAML map with a `jobs:` key"}
	}
	for _, k := range doc.keys {
		if !topKeys[k] {
			add("unknown top-level key %q", k)
		}
	}
	jobsRaw, ok := doc.m["jobs"].(*obj)
	if !ok || len(jobsRaw.keys) == 0 {
		return nil, append(errs, "`jobs:` must define at least one job")
	}

	// triggers
	on := On{Push: nil, Schedule: []string{}, Manual: true}
	on.Enabled.Push = true
	if onV, has := doc.get("on"); has {
		if err := func() error {
			o, isMap := onV.(*obj)
			if !isMap {
				return errors.New("on must be a map (push, schedule, manual)")
			}
			push, hasPush := o.get("push")
			on.Enabled.Push = hasPush && push != false
			if p, isMap := push.(*obj); isMap {
				if b, has := p.get("branches"); has {
					list, err := strList(b, "on.push.branches")
					if err != nil {
						return err
					}
					on.Push = list
				}
			}
			sched, err := strList(o.m["schedule"], "on.schedule")
			if err != nil {
				return err
			}
			on.Schedule = sched
			for _, c := range sched {
				if len(strings.Fields(c)) != 5 {
					return fmt.Errorf("on.schedule: %q must have 5 cron fields", c)
				}
			}
			on.Manual = o.m["manual"] != false
			return nil
		}(); err != nil {
			errs = append(errs, err.Error())
		}
	}

	globalVars, err := strMap(doc.m["variables"], "variables")
	if err != nil {
		errs = append(errs, err.Error())
		globalVars = map[string]string{}
	}

	defaults, _ := doc.m["defaults"].(*obj)
	if defaults == nil {
		defaults = &obj{m: map[string]any{}}
	}
	for _, k := range defaults.keys {
		if !defaultKeys[k] {
			add("defaults: unknown key %q", k)
		}
	}

	var stagesDeclared []string
	if s, has := doc.get("stages"); has {
		list, err := strList(s, "stages")
		if err != nil {
			errs = append(errs, err.Error())
		}
		stagesDeclared = list
	}
	stages := slices.Clone(DefaultStages)
	if stagesDeclared != nil {
		stages = slices.Clone(stagesDeclared)
	}
	var jobs []JobSpec

	for _, base := range jobsRaw.keys {
		specs, err := parseJob(base, jobsRaw.m[base], defaults, &stages, stagesDeclared != nil, readTemplate)
		if err != nil {
			add("job %q: %s", base, err)
			continue
		}
		jobs = append(jobs, specs...)
	}

	// needs must point to existing jobs and form a DAG
	bases := map[string]bool{}
	var baseOrder []string
	for _, j := range jobs {
		if !bases[j.Base] {
			bases[j.Base] = true
			baseOrder = append(baseOrder, j.Base)
		}
	}
	for _, j := range jobs {
		for _, n := range j.Needs {
			if !bases[n] {
				add("job %q: needs unknown job %q", j.Base, n)
			}
		}
	}
	if len(errs) == 0 {
		state := map[string]int{}
		var visit func(b string, path []string) error
		visit = func(b string, path []string) error {
			if state[b] == 2 {
				return nil
			}
			if state[b] == 1 {
				return fmt.Errorf("dependency cycle: %s", strings.Join(append(path, b), " → "))
			}
			state[b] = 1
			for _, j := range jobs {
				if j.Base == b {
					for _, n := range j.Needs {
						if err := visit(n, append(slices.Clone(path), b)); err != nil {
							return err
						}
					}
					break
				}
			}
			state[b] = 2
			return nil
		}
		for _, b := range baseOrder {
			if err := visit(b, nil); err != nil {
				errs = append(errs, err.Error())
				break
			}
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}
	used := []string{}
	for _, s := range stages {
		if slices.ContainsFunc(jobs, func(j JobSpec) bool { return j.Stage == s }) {
			used = append(used, s)
		}
	}
	return &Pipeline{On: on, Variables: globalVars, Stages: used, Jobs: jobs}, nil
}

func parseJob(base string, value any, defaults *obj, stages *[]string, declared bool, read TemplateReader) ([]JobSpec, error) {
	if !jobNameRe.MatchString(base) {
		return nil, errors.New("name may only use letters, digits, space, '.', '_' and '-'")
	}
	v, ok := value.(*obj)
	if !ok {
		return nil, errors.New("must be a map")
	}
	for _, k := range v.keys {
		if !jobKeys[k] {
			return nil, fmt.Errorf("unknown key %q", k)
		}
	}
	raw, err := applyTemplate(v, read)
	if err != nil {
		return nil, err
	}
	pick := func(k string) any {
		if x, has := raw.m[k]; has {
			return x
		}
		return defaults.m[k]
	}
	pickOr := func(k string, def any) any {
		if x := pick(k); x != nil {
			return x
		}
		return def
	}

	stage := "test"
	if s, has := raw.get("stage"); has {
		stage = jsString(s)
	}
	if !slices.Contains(*stages, stage) {
		if declared {
			return nil, fmt.Errorf("stage %q is not listed in stages", stage)
		}
		*stages = append(*stages, stage)
	}
	when, _ := raw.m["when"].(*obj)
	if when == nil {
		when = &obj{m: map[string]any{}}
	}
	status := "success"
	if s, has := when.get("status"); has && s != nil {
		status = jsString(s)
	}
	if !slices.Contains([]string{"success", "failure", "always"}, status) {
		return nil, errors.New("when.status must be success, failure or always")
	}
	image := jsString(pickOr("image", "base"))
	if !imageRe.MatchString(image) {
		return nil, fmt.Errorf("invalid image name %q", image)
	}
	disk := jsString(pickOr("disk", "10G"))
	if !diskRe.MatchString(disk) {
		return nil, fmt.Errorf("disk must look like 10G (got %q)", disk)
	}

	mainSteps, err := parseSteps(raw.m["steps"], "steps")
	if err != nil {
		return nil, err
	}
	if len(mainSteps) == 0 {
		return nil, errors.New("needs at least one step in `steps:`")
	}

	var axes []matrixAxis
	if m, has := raw.get("matrix"); has {
		mo, isMap := m.(*obj)
		if !isMap {
			return nil, errors.New("matrix must be a map of lists")
		}
		for _, k := range mo.keys {
			if !nameRe.MatchString(k) {
				return nil, fmt.Errorf("matrix: invalid key %q", k)
			}
			vals, err := strList(mo.m[k], "matrix."+k)
			if err != nil {
				return nil, err
			}
			if len(vals) == 0 {
				return nil, fmt.Errorf("matrix.%s is empty", k)
			}
			axes = append(axes, matrixAxis{k, vals})
		}
	}
	combos := cartesian(axes)
	if len(combos) > 64 {
		return nil, errors.New("matrix expands to more than 64 jobs")
	}

	vcpus := jsNumber(pickOr("vcpus", int64(2)))
	mem := jsNumber(pickOr("mem", int64(2048)))
	if !isInteger(vcpus) || vcpus < 1 || vcpus > 32 {
		return nil, errors.New("vcpus must be 1-32")
	}
	if !isInteger(mem) || mem < 256 || mem > 65536 {
		return nil, errors.New("mem must be 256-65536 (MiB)")
	}
	timeout, err := ParseDuration(pick("timeout"), 3600)
	if err != nil {
		return nil, err
	}
	var needs []string
	if n, has := raw.get("needs"); has {
		if needs, err = strList(n, "needs"); err != nil {
			return nil, err
		}
	}
	jobVars, err := strMap(raw.m["variables"], "variables")
	if err != nil {
		return nil, err
	}
	secrets, err := strList(raw.m["secrets"], "secrets")
	if err != nil {
		return nil, err
	}
	before, err := parseSteps(pick("before"), "before")
	if err != nil {
		return nil, err
	}
	after, err := parseSteps(pick("after"), "after")
	if err != nil {
		return nil, err
	}
	artifacts, err := parseArtifacts(raw.m["artifacts"])
	if err != nil {
		return nil, err
	}
	caches, err := parseCaches(raw.m["cache"], base)
	if err != nil {
		return nil, err
	}
	var branches, events []string
	if b, has := when.get("branches"); has {
		if branches, err = strList(b, "when.branches"); err != nil {
			return nil, err
		}
	}
	if e, has := when.get("events"); has {
		if events, err = strList(e, "when.events"); err != nil {
			return nil, err
		}
	}
	retry := jsNumber(pickOr("retry", int64(0)))
	depth := jsNumber(pickOr("depth", int64(50)))
	if math.IsNaN(retry) {
		retry = 0
	}
	if math.IsNaN(depth) {
		depth = 50
	}

	out := make([]JobSpec, 0, len(combos))
	for _, combo := range combos {
		name := base
		var vals []string
		for _, a := range axes {
			vals = append(vals, combo[a.key])
		}
		if len(vals) > 0 {
			name += " (" + strings.Join(vals, ", ") + ")"
		}
		vars := make(map[string]string, len(jobVars)+len(combo))
		for k, x := range jobVars {
			vars[k] = x
		}
		for k, x := range combo {
			vars["MATRIX_"+strings.ToUpper(k)] = x
		}
		out = append(out, JobSpec{
			Base: base, Name: name, Stage: stage, Image: image, Vcpus: int(vcpus), Mem: int(mem), Disk: disk,
			TimeoutS: timeout, Needs: needs, Matrix: combo, Variables: vars, Secrets: secrets,
			Before: before, Steps: mainSteps, After: after, Artifacts: artifacts, Cache: caches,
			When:   WhenSpec{Branches: branches, Events: events, Status: status},
			Manual: raw.m["manual"] == true, AllowFailure: raw.m["allow_failure"] == true,
			Retry: int(math.Min(math.Max(retry, 0), 5)), Depth: int(math.Max(depth, 0)),
		})
	}
	return out, nil
}

// Dependencies returns the indexes each job waits for: its explicit needs, or every job of an
// earlier stage.
func Dependencies(jobs []JobSpec, stages []string) [][]int {
	out := make([][]int, len(jobs))
	for i, j := range jobs {
		deps := []int{}
		if j.Needs != nil {
			for k, o := range jobs {
				if slices.Contains(j.Needs, o.Base) {
					deps = append(deps, k)
				}
			}
		} else {
			rank := slices.Index(stages, j.Stage)
			for k, o := range jobs {
				if slices.Index(stages, o.Stage) < rank {
					deps = append(deps, k)
				}
			}
		}
		out[i] = deps
	}
	return out
}
