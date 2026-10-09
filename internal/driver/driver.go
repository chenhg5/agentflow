package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Provider struct {
	Command []string `json:"command"`
}

type CommandOptions struct {
	Dir       string
	Env       map[string]string
	Stdin     string
	TimeoutMs int
}

type CommandResult struct {
	Code     int
	Signal   string
	Stdout   string
	Stderr   string
	TimedOut bool
}

func LoadProviders(cwd string) (map[string]Provider, error) {
	files := []string{filepath.Join(cwd, ".agentflow", "providers.json")}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".agentflow", "providers.json"))
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read providers file %s: %w", file, err)
		}
		var config struct {
			Drivers map[string]Provider `json:"drivers"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("invalid providers file %s: %w", file, err)
		}
		return config.Drivers, nil
	}
	return map[string]Provider{}, nil
}

func RunCommand(command []string, options CommandOptions) CommandResult {
	if len(command) == 0 {
		return CommandResult{Code: 1, Stderr: "command must be non-empty"}
	}
	ctx := context.Background()
	var cancel context.CancelFunc
	if options.TimeoutMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(options.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = options.Dir
	cmd.Env = os.Environ()
	for key, value := range options.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if options.Stdin != "" {
		cmd.Stdin = strings.NewReader(options.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := CommandResult{Code: 0, Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		result.Code = 124
		result.TimedOut = true
		return result
	}
	if err == nil {
		return result
	}
	result.Code = 1
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.Code = exitErr.ExitCode()
		if exitErr.ProcessState != nil && exitErr.ProcessState.Sys() != nil {
			result.Signal = exitErr.ProcessState.String()
		}
	}
	return result
}

func ExpandCommand(command []string, prompt, workspace, resultFile, runID, nodeID string) []string {
	result := make([]string, len(command))
	for index, part := range command {
		result[index] = strings.NewReplacer("{{ prompt }}", prompt, "{{workspace}}", workspace, "{{ result_file }}", resultFile, "{{ run_id }}", runID, "{{ node_id }}", nodeID).Replace(part)
	}
	return result
}
