package driver

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
)

// EnvironmentPolicy controls host-variable inheritance, not filesystem or
// process isolation. Values are never part of policy diagnostics.
type EnvironmentPolicy struct {
	Mode    string   `json:"mode"`
	Inherit []string `json:"inherit,omitempty"`
}

func reservedEnvironmentName(name string) bool {
	return strings.HasPrefix(strings.ToUpper(name), "AGENTFLOW_")
}

func validEnvironmentName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "=\x00\r\n")
}

func validateEnvironmentPolicy(policy *EnvironmentPolicy, windows bool) error {
	if policy == nil {
		return nil
	}
	switch policy.Mode {
	case "inherit", "empty":
		if len(policy.Inherit) != 0 {
			return fmt.Errorf("environment.inherit requires mode allowlist")
		}
	case "allowlist":
	default:
		return fmt.Errorf("environment.mode must be inherit, allowlist, or empty")
	}
	seen := map[string]bool{}
	for _, name := range policy.Inherit {
		if !validEnvironmentName(name) || reservedEnvironmentName(name) {
			return fmt.Errorf("environment.inherit contains an invalid or reserved variable name")
		}
		key := environmentKey(name, windows)
		if seen[key] {
			return fmt.Errorf("environment.inherit contains duplicate variable names")
		}
		seen[key] = true
	}
	return nil
}

func ValidateEnvironment(executor Executor) error {
	if err := validateEnvironmentPolicy(executor.Environment, runtime.GOOS == "windows"); err != nil {
		return err
	}
	seen := map[string]bool{}
	for key, value := range executor.Env {
		if !validEnvironmentName(key) || reservedEnvironmentName(key) {
			return fmt.Errorf("executor.env contains an invalid or reserved variable name")
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("executor.env contains a value with a NUL character")
		}
		canonical := environmentKey(key, runtime.GOOS == "windows")
		if seen[canonical] {
			return fmt.Errorf("executor.env contains duplicate variable names")
		}
		seen[canonical] = true
	}
	return nil
}

func EnvironmentDescription(policy *EnvironmentPolicy) map[string]any {
	mode := "inherit"
	names := []string{}
	if policy != nil {
		mode = policy.Mode
		names = append(names, policy.Inherit...)
	}
	return map[string]any{"mode": mode, "inherit": names}
}

func environmentKey(name string, windows bool) string {
	if windows {
		return strings.ToUpper(name)
	}
	return name
}

func buildEnvironment(policy *EnvironmentPolicy, host []string, values map[string]string, windows bool) ([]string, error) {
	if err := validateEnvironmentPolicy(policy, windows); err != nil {
		return nil, err
	}
	mode := "inherit"
	allowed := map[string]bool{}
	if policy != nil {
		mode = policy.Mode
		for _, name := range policy.Inherit {
			allowed[environmentKey(name, windows)] = true
		}
	}
	entries := map[string]string{}
	for _, entry := range host {
		name, _, ok := strings.Cut(entry, "=")
		if windows && strings.HasPrefix(entry, "=") {
			name, _, ok = strings.Cut(entry[1:], "=")
			name = "=" + name
		}
		if !ok || reservedEnvironmentName(name) {
			continue
		}
		key := environmentKey(name, windows)
		if mode == "inherit" || mode == "allowlist" && allowed[key] {
			entries[key] = entry
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, name := range keys {
		value := values[name]
		key := environmentKey(name, windows)
		if !validEnvironmentName(name) || strings.ContainsRune(value, 0) || seen[key] {
			return nil, fmt.Errorf("invalid or duplicate execution environment variable")
		}
		seen[key] = true
		entries[key] = name + "=" + value
	}
	keys = keys[:0]
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// A non-nil empty slice asks os/exec for an empty environment rather than
	// falling back to os.Environ. Windows may add SystemRoot as required by Go.
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, entries[key])
	}
	return env, nil
}

func executionEnvironment(policy *EnvironmentPolicy, values map[string]string) ([]string, error) {
	return buildEnvironment(policy, os.Environ(), values, runtime.GOOS == "windows")
}
