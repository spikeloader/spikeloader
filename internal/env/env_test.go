package env

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	text := "# comment\nVM_VCPUS=2\nGUEST_KERNEL=\"7.3\"\nGUEST_PACKAGES=\"base openssh\"\nEMPTY=\n lower=ignored\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	want := map[string]string{"VM_VCPUS": "2", "GUEST_KERNEL": "7.3", "GUEST_PACKAGES": "base openssh", "EMPTY": ""}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadFile = %v, %v; want %v", got, err, want)
	}
	_, err = ReadFile(path + ".missing")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing config error = %v", err)
	}
}
