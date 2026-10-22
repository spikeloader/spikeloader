package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spikeloader/internal/env"
)

func TestJobLogLimitAndMarkers(t *testing.T) {
	oldRoot := env.Root
	env.Root = t.TempDir()
	t.Cleanup(func() { env.Root = oldRoot })
	if err := os.WriteFile(filepath.Join(env.Root, ".env"), []byte("CI_MAX_LOG_MB=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "job.log")
	var events []string
	l, err := newJobLog(file, []string{"top-secret"}, func(kind string, idx, rc int) { events = append(events, kind) }, func(int) string { return "test" })
	if err != nil {
		t.Fatal(err)
	}
	l.Write("##[step] 0\nsecret=top-secret\n")
	// Exercise a huge unterminated line as well as ordinary lines.
	for i := 0; i < 128; i++ {
		l.Write(strings.Repeat("x", 32768))
	}
	l.Write("\n##[end] 0 0\n##[step] 1\n##[end] 1 7\n##[skip] 2\n")
	l.Close()
	if l.err != nil {
		t.Fatal(l.err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 1<<20 || strings.Count(string(b), "log truncated at 1 MB") != 1 {
		t.Fatalf("size=%d truncations=%d", len(b), strings.Count(string(b), "log truncated"))
	}
	if strings.Contains(string(b), "top-secret") || !strings.Contains(string(b), "secret=***") {
		t.Fatal("masking changed")
	}
	if strings.Join(events, ",") != "step,end,step,end,skip" {
		t.Fatal(events)
	}
	l, err = newJobLog(file, nil, func(string, int, int) {}, func(int) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	l.Write(strings.Repeat("z", 2<<20))
	l.Close()
	b, _ = os.ReadFile(file)
	if len(b) > 1<<20 || strings.Count(string(b), "log truncated") != 1 {
		t.Fatal("reopen exceeded cap or duplicated notice")
	}
}
