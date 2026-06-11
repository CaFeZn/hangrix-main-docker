package loop

import "fmt"

// expandEnv replaces ${VAR} references inside the env map with the value
// from repoVars. Only full-value references (e.g. FOO: ${BAR}) are
// expanded; partial references like FOO: prefix-${BAR} and non-references
// pass through unchanged. The variable name between ${ and } must match
// [A-Za-z_][A-Za-z0-9_]*.
//
// Missing variables return an error naming the first missing name and
// the env key that referenced it. The caller MUST fail the session on
// error — silently injecting an empty string would mask configuration
// mistakes.
//
// Used by the workflow driver to expand variable references in
// container.env, job.env, and step.env at job-claim time.
func expandEnv(env map[string]string, repoVars map[string]string) error {
	if repoVars == nil {
		return nil // server hasn't been updated yet — backward compat
	}
	for k, v := range env {
		if len(v) < 4 || v[0] != '$' || v[1] != '{' || v[len(v)-1] != '}' {
			continue
		}
		varName := v[2 : len(v)-1]
		if !isEnvVarName(varName) {
			continue
		}
		replacement, ok := repoVars[varName]
		if !ok {
			return fmt.Errorf("env %q references undefined variable %q", k, varName)
		}
		env[k] = replacement
	}
	return nil
}

// isEnvVarName reports whether s is a valid variable name for ${...}
// expansion: non-empty, starts with a letter or underscore, and contains
// only letters, digits, and underscores.
func isEnvVarName(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, c := range s {
		if i == 0 {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_') {
				return false
			}
		} else {
			if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
				return false
			}
		}
	}
	return true
}
