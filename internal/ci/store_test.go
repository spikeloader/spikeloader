package ci

import (
	"os"
	"sync"
	"testing"
)

func TestSealRoundTrip(t *testing.T) {
	keyOnce = sync.Once{}
	secretsKeyFile = t.TempDir() + "/secrets.key"
	v, err := seal("kube-token-ünïcode")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := unseal(v); err != nil || got != "kube-token-ünïcode" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := unseal("legacy-plain"); got != "legacy-plain" {
		t.Fatal("legacy plaintext")
	}
	if _, err := unseal(v[:len(v)-4] + "AAAA"); err == nil {
		t.Fatal("tampered value accepted")
	}
}

// A value written by the TypeScript panel (same key file format) must still decrypt.
func TestUnsealTypeScriptVector(t *testing.T) {
	keyOnce = sync.Once{}
	secretsKeyFile = t.TempDir() + "/secrets.key"
	if err := os.WriteFile(secretsKeyFile, []byte("BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := unseal("v1:CQkJCQkJCQkJCQkJgtw2bzO9K7Wd2ZeUzyH1eUH36/mThLgRxRGoSo+a1w==")
	if err != nil || got != "from-typescript" {
		t.Fatalf("%q %v", got, err)
	}
}
