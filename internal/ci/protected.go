package ci

import (
	"strings"
)

func validateProtectedBranches(patterns []string) error {
	if len(patterns) > 100 {
		return fail(400, "at most 100 protected branch patterns are allowed")
	}
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) != pattern || pattern == "" || len(pattern) > 255 || strings.ContainsAny(pattern, " \t\r\n\\") || strings.HasPrefix(pattern, "refs/") {
			return fail(400, "invalid protected branch pattern %q", pattern)
		}
	}
	return nil
}

func selectSecrets(p Project, r Run, requested []string, all Secrets) (map[string]string, error) {
	selected := map[string]string{}
	patterns := p.ProtectedBranches
	if patterns == nil {
		patterns = []string{p.DefaultBranch}
	}
	trusted := r.SourceRef == "refs/heads/"+r.Branch && anyGlob(patterns, r.Branch)
	for _, name := range requested {
		secret, ok := all[p.ID][name]
		if !ok {
			secret, ok = all["_global"][name]
		}
		if !ok {
			return nil, fail(400, "secret %s is not configured", name)
		}
		if secret.Protected && !trusted {
			return nil, fail(400, "secret %s is only available on protected branches", name)
		}
		selected[name] = secret.Value
	}
	return selected, nil
}
