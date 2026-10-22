package ci

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func noTemplates(string) (string, bool) { return "", false }

func TestParseOrderStagesAndDeps(t *testing.T) {
	p, errs := ParsePipeline(`
variables: { GREETING: hi, N: 3 }
jobs:
  zeta:
    stage: build
    steps: [make]
  alpha:
    steps:
      - go test ./...
      - { name: lint, run: "golangci-lint run\nmore" }
    matrix: { os: [arch, alpine], v: [1, 2] }
  deploy:
    stage: deploy
    needs: [zeta]
    manual: true
    when: { branches: [main, "release/**"] }
    steps: deploy.sh
`, noTemplates)
	if errs != nil {
		t.Fatal(errs)
	}
	var names []string
	for _, j := range p.Jobs {
		names = append(names, j.Name)
	}
	want := "zeta|alpha (arch, 1)|alpha (arch, 2)|alpha (alpine, 1)|alpha (alpine, 2)|deploy"
	if got := strings.Join(names, "|"); got != want {
		t.Fatalf("job order: %s", got)
	}
	if got := strings.Join(p.Stages, ","); got != "build,test,deploy" {
		t.Fatalf("stages: %s", got)
	}
	if p.Variables["N"] != "3" {
		t.Fatalf("variables: %v", p.Variables)
	}
	a := p.Jobs[1]
	if a.Stage != "test" || a.Image != "base" || a.Vcpus != 2 || a.Mem != 2048 || a.Disk != "10G" || a.TimeoutS != 3600 || a.Depth != 50 {
		t.Fatalf("defaults: %+v", a)
	}
	if a.Variables["MATRIX_OS"] != "arch" || a.Steps[1].Name != "lint" || a.Needs != nil {
		t.Fatalf("job: %+v", a)
	}
	deps := Dependencies(p.Jobs, p.Stages)
	if len(deps[0]) != 0 || len(deps[1]) != 1 || deps[1][0] != 0 || len(deps[5]) != 1 || deps[5][0] != 0 {
		t.Fatalf("deps: %v", deps)
	}
	d := p.Jobs[5]
	if !d.Manual || d.When.Branches[1] != "release/**" || d.When.Events != nil {
		t.Fatalf("deploy: %+v", d)
	}
}

func TestJSONShape(t *testing.T) {
	p, errs := ParsePipeline("jobs:\n  a:\n    steps: [x]\n", noTemplates)
	if errs != nil {
		t.Fatal(errs)
	}
	b, _ := json.Marshal(p.Jobs[0])
	s := string(b)
	for _, want := range []string{`"needs":null`, `"secrets":[]`, `"before":[]`, `"after":[]`, `"artifacts":null`, `"cache":[]`, `"matrix":{}`, `"branches":null`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	b, _ = json.Marshal(p.On)
	if string(b) != `{"push":null,"schedule":[],"manual":true,"enabled":{"push":true}}` {
		t.Errorf("on: %s", b)
	}
}

func TestErrors(t *testing.T) {
	cases := map[string]string{
		"jobs: {}":                                  "`jobs:` must define at least one job",
		"foo: 1\njobs:\n  a: { steps: [x] }":        `unknown top-level key "foo"`,
		"jobs:\n  a: { steps: [] }":                 "needs at least one step",
		"jobs:\n  a: { steps: [x], image: '../x' }": "invalid image name",
		"jobs:\n  a: { steps: [x], needs: [b] }":    `needs unknown job "b"`,
		"jobs:\n  a: { steps: [x], needs: [b] }\n  b: { steps: [x], needs: [a] }": "dependency cycle: a → b → a",
		"jobs:\n  a: { steps: [x], uses: ../../etc/x }":                           "uses: invalid template name",
		"jobs:\n  a: { steps: [x], vcpus: 64 }":                                   "vcpus must be 1-32",
		"stages: [build]\njobs:\n  a: { steps: [x] }":                             `stage "test" is not listed in stages`,
		"on: { schedule: '* *' }\njobs:\n  a: { steps: [x] }":                     "must have 5 cron fields",
		"jobs:\n  a: { steps: [x] }\n  a: { steps: [y] }":                         "duplicate key",
	}
	for src, want := range cases {
		_, errs := ParsePipeline(src, noTemplates)
		if !strings.Contains(strings.Join(errs, "\n"), want) {
			t.Errorf("%q: want %q, got %v", src, want, errs)
		}
	}
}

func TestTemplates(t *testing.T) {
	tpl := `
image: kube
inputs: { path: k8s/, namespace: default }
secrets: [KUBECONFIG_DATA]
variables: { FROM_TPL: "1" }
steps: [kubectl apply -k "$INPUT_PATH"]
`
	read := func(name string) (string, bool) { return tpl, name == "deploy/kubectl" }
	p, errs := ParsePipeline(`
jobs:
  deploy:
    uses: deploy/kubectl
    with: { namespace: prod }
    secrets: [EXTRA]
`, read)
	if errs != nil {
		t.Fatal(errs)
	}
	j := p.Jobs[0]
	if j.Image != "kube" || j.Variables["INPUT_PATH"] != "k8s/" || j.Variables["INPUT_NAMESPACE"] != "prod" || j.Variables["FROM_TPL"] != "1" {
		t.Fatalf("template merge: %+v", j)
	}
	if strings.Join(j.Secrets, ",") != "KUBECONFIG_DATA,EXTRA" {
		t.Fatalf("secrets: %v", j.Secrets)
	}
	_, errs = ParsePipeline("jobs:\n  d: { uses: deploy/kubectl, with: { nope: 1 } }", read)
	if !strings.Contains(strings.Join(errs, ""), `has no input "nope" (inputs: path, namespace)`) {
		t.Fatalf("unknown input: %v", errs)
	}
}

func TestGlobAndDuration(t *testing.T) {
	for _, c := range []struct {
		p, v string
		ok   bool
	}{{"main", "main", true}, {"release/*", "release/1.0", true}, {"release/*", "release/a/b", false}, {"release/**", "release/a/b", true}, {"feat-*", "feat-x.y", true}} {
		if GlobMatch(c.p, c.v) != c.ok {
			t.Errorf("glob %s %s", c.p, c.v)
		}
	}
	if d, _ := ParseDuration("5m", 0); d != 300 {
		t.Error("5m")
	}
	if _, err := ParseDuration("5 days", 0); err == nil {
		t.Error("bad duration accepted")
	}
}

func TestCron(t *testing.T) {
	sun := time.Date(2026, 10, 25, 3, 30, 0, 0, time.Local) // a Sunday
	for expr, want := range map[string]bool{
		"30 3 * * *": true, "*/15 * * * *": true, "*/7 * * * *": false, "30 3 * * 0": true, "30 3 * * 7": true,
		"30 3 * * 1-5": false, "0,30 1-4 25 10 *": true, "30 3 * * 6,7": true,
	} {
		if CronMatches(expr, sun) != want {
			t.Errorf("%s: want %v", expr, want)
		}
	}
}
