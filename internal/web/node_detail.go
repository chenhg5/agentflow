package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chenhg5/agentflow/internal/store"
)

const logPreviewBytes int64 = 64 * 1024

type inspectionValue struct {
	Available bool   `json:"available"`
	Value     any    `json:"value"`
	Error     string `json:"error,omitempty"`
}

type inspectionLog struct {
	Available bool   `json:"available"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

func (s *Server) handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	runID, nodeID := r.URL.Query().Get("run"), r.URL.Query().Get("node")
	if runID == "" || runID == "." || runID == ".." || strings.ContainsAny(runID, `/\`) || nodeID == "" {
		writeError(w, http.StatusBadRequest, "run and node identifiers are required")
		return
	}
	run, err := store.LoadRun(s.options.Root, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	state := run.State.Nodes[nodeID]
	visit := run.State.Visits[nodeID]
	if requested := r.URL.Query().Get("visit"); requested != "" && requested != "current" {
		number, err := strconv.Atoi(requested)
		if err != nil || number < 1 {
			writeError(w, http.StatusBadRequest, "visit must be a positive integer or current")
			return
		}
		if number != visit {
			state = nil
			for _, previous := range run.State.History {
				if previous.NodeID == nodeID && previous.Visit == number {
					copy := previous.State
					state = &copy
					break
				}
			}
		}
		visit = number
	}
	if state == nil {
		writeError(w, http.StatusNotFound, "node visit not found")
		return
	}
	writeJSON(w, map[string]any{
		"run_id": runID, "node_id": nodeID, "visit": visit, "state": state,
		"input":  inspectValue(run.RunDir, state.Input, state.Artifacts["input"]),
		"output": inspectValue(run.RunDir, state.Result, state.Artifacts["result"]),
		"logs": map[string]inspectionLog{
			"stdout": inspectLog(run.RunDir, state.Artifacts["stdoutLog"]),
			"stderr": inspectLog(run.RunDir, state.Artifacts["stderrLog"]),
		},
	})
}

// Only files registered as artifacts for this visit may be read, and symlinks
// must still resolve within this run. Request parameters never supply paths.
func inspectionArtifact(runDir, reference string) (*os.File, error) {
	if reference == "" {
		return nil, os.ErrNotExist
	}
	base, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return nil, err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(base, reference))
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, fmt.Errorf("artifact outside this run")
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("artifact is not a regular file")
	}
	return file, nil
}

func inspectValue(runDir string, value any, reference string) inspectionValue {
	if value != nil {
		return inspectionValue{Available: true, Value: value}
	}
	file, err := inspectionArtifact(runDir, reference)
	if os.IsNotExist(err) {
		return inspectionValue{}
	}
	if err != nil {
		return inspectionValue{Error: "Cannot read recorded artifact"}
	}
	defer file.Close()
	// A JSON null is a recorded value too, even though state omits nil fields.
	if err := json.NewDecoder(file).Decode(&value); err != nil {
		return inspectionValue{Error: "Recorded artifact is not valid JSON"}
	}
	return inspectionValue{Available: true, Value: value}
}

func inspectLog(runDir, reference string) inspectionLog {
	file, err := inspectionArtifact(runDir, reference)
	if os.IsNotExist(err) {
		return inspectionLog{}
	}
	if err != nil {
		return inspectionLog{Error: "Cannot read recorded log"}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return inspectionLog{Error: "Cannot read recorded log"}
	}
	truncated := info.Size() > logPreviewBytes
	if truncated {
		if _, err := file.Seek(-logPreviewBytes, io.SeekEnd); err != nil {
			return inspectionLog{Error: "Cannot read recorded log"}
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, logPreviewBytes))
	if err != nil {
		return inspectionLog{Error: "Cannot read recorded log"}
	}
	return inspectionLog{Available: true, Text: string(data), Truncated: truncated}
}
