package ci

import "testing"

func TestProtectedSecrets(t *testing.T) {
	p := Project{ID: "demo", DefaultBranch: "main", ProtectedBranches: []string{"main", "release/*"}}
	secrets := Secrets{"_global": {"TOKEN": {Value: "global", Protected: true}}, "demo": {"PUBLIC": {Value: "public"}}}
	for _, tc := range []struct {
		branch, ref string
		allowed     bool
	}{
		{"main", "refs/heads/main", true},
		{"release/v1", "refs/heads/release/v1", true},
		{"feature", "refs/heads/feature", false},
		{"main", "refs/tags/main", false},
		{"main", "", false},
		{"main", "refs/heads/feature", false},
	} {
		r := Run{Branch: tc.branch, SourceRef: tc.ref}
		got, err := selectSecrets(p, r, []string{"TOKEN", "PUBLIC"}, secrets)
		if tc.allowed {
			if err != nil || got["TOKEN"] != "global" || got["PUBLIC"] != "public" {
				t.Fatalf("%+v: %v %v", tc, got, err)
			}
		} else if err == nil || err.Error() != "secret TOKEN is only available on protected branches" {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
	secrets["demo"]["TOKEN"] = Secret{Value: "override", Protected: false}
	got, err := selectSecrets(p, Run{Branch: "feature"}, []string{"TOKEN"}, secrets)
	if err != nil || got["TOKEN"] != "override" {
		t.Fatalf("override: %v %v", got, err)
	}
	delete(secrets["demo"], "TOKEN")
	p.ProtectedBranches = []string{}
	if _, err := selectSecrets(p, Run{Branch: "main", SourceRef: "refs/heads/main"}, []string{"TOKEN"}, secrets); err == nil {
		t.Fatal("empty policy allowed secret")
	}
	p.ProtectedBranches = nil
	if _, err := selectSecrets(p, Run{Branch: "main", SourceRef: "refs/heads/main"}, []string{"TOKEN"}, secrets); err != nil {
		t.Fatal(err)
	}
	if _, err := selectSecrets(p, Run{}, []string{"MISSING"}, secrets); err == nil {
		t.Fatal("missing secret accepted")
	}
}

func TestProtectedBranchDefaults(t *testing.T) {
	p := Project{DefaultBranch: "trunk"}
	p.normalize()
	if len(p.ProtectedBranches) != 1 || p.ProtectedBranches[0] != "trunk" {
		t.Fatal(p.ProtectedBranches)
	}
	for _, patterns := range [][]string{{""}, {" main"}, {"refs/heads/main"}, {"bad\npattern"}} {
		if err := validateProtectedBranches(patterns); err == nil {
			t.Fatalf("accepted %q", patterns)
		}
	}
}
