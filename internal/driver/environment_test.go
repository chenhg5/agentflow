package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestEnvironmentChildProcess(t *testing.T) {
	if os.Getenv("AF_ENV_HELPER") != "1" {
		return
	}
	_, visible := os.LookupEnv("AF_CANARY_VISIBLE")
	_, secret := os.LookupEnv("AF_CANARY_SECRET")
	_, channel := os.LookupEnv("AGENTFLOW_RESULT_FILE")
	data, _ := json.Marshal(map[string]bool{"visible": visible, "secret": secret, "channel": channel})
	fmt.Print(string(data))
	os.Exit(0)
}

func TestEnvironmentPolicyControlsRealChild(t *testing.T) {
	t.Setenv("AF_CANARY_VISIBLE", "synthetic-visible")
	t.Setenv("AF_CANARY_SECRET", "synthetic-secret")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name            string
		policy          *EnvironmentPolicy
		visible, secret bool
	}{
		{"default", nil, true, true},
		{"allowlist", &EnvironmentPolicy{Mode: "allowlist", Inherit: []string{"AF_CANARY_VISIBLE"}}, true, false},
		{"empty", &EnvironmentPolicy{Mode: "empty"}, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := RunCommand([]string{binary, "-test.run=^TestEnvironmentChildProcess$"}, CommandOptions{Environment: test.policy, Env: map[string]string{"AF_ENV_HELPER": "1", "AGENTFLOW_RESULT_FILE": "channel"}})
			var observed map[string]bool
			if result.Code != 0 || json.Unmarshal([]byte(result.Stdout), &observed) != nil {
				t.Fatalf("child failed: code=%d", result.Code)
			}
			if observed["visible"] != test.visible || observed["secret"] != test.secret || !observed["channel"] {
				t.Fatalf("wrong environment policy result: %v", observed)
			}
		})
	}
}

func TestWindowsEnvironmentNamesAndReservedHostValues(t *testing.T) {
	policy := &EnvironmentPolicy{Mode: "allowlist", Inherit: []string{"Path"}}
	values, err := buildEnvironment(policy, []string{"PATH=host", "AF_SECRET=hidden", "AGENTFLOW_NODE_TOKEN=stale"}, map[string]string{"path": "explicit", "AGENTFLOW_NODE_TOKEN": "fresh"}, true)
	if err != nil || len(values) != 2 {
		t.Fatalf("unexpected Windows environment: count=%d err=%v", len(values), err)
	}
	for _, value := range values {
		if value != "path=explicit" && value != "AGENTFLOW_NODE_TOKEN=fresh" {
			t.Fatal("stale or unselected host variable survived")
		}
	}
	if _, err := buildEnvironment(&EnvironmentPolicy{Mode: "allowlist", Inherit: []string{"PATH", "Path"}}, nil, nil, true); err == nil {
		t.Fatal("Windows duplicate names accepted")
	}
	if _, err := buildEnvironment(nil, nil, map[string]string{"PATH": "a", "Path": "b"}, true); err == nil {
		t.Fatal("Windows duplicate explicit names accepted")
	}
	values, err = buildEnvironment(&EnvironmentPolicy{Mode: "empty"}, nil, nil, false)
	if err != nil || values == nil || len(values) != 0 {
		t.Fatal("empty policy must pass a non-nil empty environment")
	}
	values, err = buildEnvironment(nil, []string{"AGENTFLOW_NODE_TOKEN=stale", "USER=ordinary"}, nil, false)
	if err != nil || len(values) != 1 || values[0] != "USER=ordinary" {
		t.Fatal("reserved parent invocation leaked into child")
	}
}

func TestEnvironmentConfigValidation(t *testing.T) {
	for _, policy := range []*EnvironmentPolicy{{Mode: "unknown"}, {Mode: "inherit", Inherit: []string{"HOME"}}, {Mode: "empty", Inherit: []string{"HOME"}}, {Mode: "allowlist", Inherit: []string{"AGENTFLOW_NODE_TOKEN"}}, {Mode: "allowlist", Inherit: []string{"BAD=NAME"}}, {Mode: "allowlist", Inherit: []string{"HOME", "HOME"}}} {
		if err := ValidateEnvironment(Executor{Environment: policy}); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	for _, vars := range []map[string]string{{"AGENTFLOW_RESULT_FILE": "bad"}, {"agentflow_node_token": "bad"}, {"BAD=NAME": "bad"}, {"VALID": "bad\x00value"}} {
		if err := ValidateEnvironment(Executor{Env: vars}); err == nil {
			t.Fatal("invalid executor env accepted")
		}
	}
}
