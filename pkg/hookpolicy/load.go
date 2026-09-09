package hookpolicy

// LoadPolicy loads global profile instructions, then workspace instructions and YAML overrides.
// Precedence: workspace hooks.yaml overrides workspace HOOKS.md, which extends profile HOOKS.md.
func LoadPolicy(profileDir, workspace string) (Policy, Diagnostics, error) {
	policy := defaultPolicy()
	diag := Diagnostics{}

	if err := parseNaturalLanguagePolicy(profileDir, &policy, &diag); err != nil {
		return Policy{}, diag, err
	}
	if err := parseNaturalLanguagePolicy(workspace, &policy, &diag); err != nil {
		return Policy{}, diag, err
	}
	if err := parseYAMLOverrides(workspace, &policy, &diag); err != nil {
		return Policy{}, diag, err
	}

	return policy, diag, nil
}
