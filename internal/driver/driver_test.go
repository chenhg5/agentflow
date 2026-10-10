package driver

import "testing"

func TestExpandCommandIncludesSessionAndWorkspace(t *testing.T) {
	got := ExpandCommand([]string{"agent", "{{ session_id }}", "{{ workspace }}", "{{ prompt }}", "{{result_file}}"}, "fix it", "/repo", "session-123", "/run/result.json", "run-1", "node-1")
	want := []string{"agent", "session-123", "/repo", "fix it", "/run/result.json"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
