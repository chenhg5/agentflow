package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/graph"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
)

//go:embed ui/index.html
var uiFiles embed.FS

type Options struct {
	Root         string
	WorkflowPath string
	RunID        string
	Host         string
	Port         int
}

type Server struct {
	options Options
	mux     *http.ServeMux
}

func NewServer(options Options) *Server {
	if options.Host == "" {
		options.Host = "127.0.0.1"
	}
	if options.Port < 0 {
		options.Port = 7070
	}
	server := &Server{options: options, mux: http.NewServeMux()}
	server.mux.HandleFunc("/", server.handleIndex)
	server.mux.HandleFunc("/api/state", server.handleState)
	server.mux.HandleFunc("/api/runs", server.handleRuns)
	server.mux.HandleFunc("/api/runs/", server.handleRun)
	server.mux.HandleFunc("/api/node", server.handleNodeDetail)
	return server
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) Serve() error {
	listener, err := net.Listen("tcp", net.JoinHostPort(s.options.Host, fmt.Sprintf("%d", s.options.Port)))
	if err != nil {
		return err
	}
	defer listener.Close()
	address := listener.Addr().String()
	if s.options.Host == "0.0.0.0" {
		address = "localhost:" + fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)
	}
	fmt.Fprintf(os.Stderr, "AgentFlow web: http://%s/\n", address)
	return http.Serve(listener, s.mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := uiFiles.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "web UI is unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run")
	if runID == "" {
		runID = s.options.RunID
	}
	if runID != "" {
		run, err := store.LoadRun(s.options.Root, runID)
		if err != nil {
			writeError(w, http.StatusNotFound, fmt.Sprintf("run not found: %s", runID))
			return
		}
		writeJSON(w, runView(run, s.options.Root))
		return
	}
	if s.options.WorkflowPath != "" {
		workflow, err := loadWorkflow(s.options.WorkflowPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"mode":             "workflow",
			"source":           s.options.WorkflowPath,
			"workflow":         workflow,
			"graph":            graph.Describe(workflow),
			"graph_validation": graph.Validate(workflow),
		})
		return
	}
	writeJSON(w, dashboardView(s.options.Root))
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/runs" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, dashboardView(s.options.Root))
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	if runID == "" || strings.Contains(runID, "/") {
		http.NotFound(w, r)
		return
	}
	run, err := store.LoadRun(s.options.Root, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("run not found: %s", runID))
		return
	}
	writeJSON(w, runView(run, s.options.Root))
}

func loadWorkflow(path string) (model.Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.Workflow{}, fmt.Errorf("cannot read workflow %s: %w", path, err)
	}
	var workflow model.Workflow
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&workflow); err != nil {
		return model.Workflow{}, fmt.Errorf("invalid workflow JSON: %w", err)
	}
	if problems := schema.ValidateWorkflowJSON(data); len(problems) > 0 {
		return model.Workflow{}, fmt.Errorf("invalid workflow: %s", strings.Join(problems, "; "))
	}
	if problems := schema.Validate(workflow); len(problems) > 0 {
		return model.Workflow{}, fmt.Errorf("invalid workflow: %s", strings.Join(problems, "; "))
	}
	return workflow, nil
}

func dashboardView(root string) map[string]any {
	entries, err := os.ReadDir(filepath.Join(root, "runs"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"mode": "dashboard", "runs": []any{}}
		}
		return map[string]any{"mode": "dashboard", "runs": []any{}, "error": err.Error()}
	}
	type runSummary struct {
		RunID     string `json:"run_id"`
		Workflow  string `json:"workflow"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	runs := make([]runSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		run, err := store.LoadRun(root, entry.Name())
		if err != nil {
			continue
		}
		runs = append(runs, runSummary{
			RunID: run.RunID, Workflow: run.State.WorkflowName, Status: run.State.Status,
			CreatedAt: run.State.CreatedAt, UpdatedAt: run.State.UpdatedAt,
		})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].UpdatedAt > runs[j].UpdatedAt })
	return map[string]any{"mode": "dashboard", "runs": runs}
}

func runView(run *model.Run, root string) map[string]any {
	view := map[string]any{
		"mode":             "run",
		"workflow":         run.Workflow,
		"graph":            graph.Describe(run.Workflow),
		"graph_validation": graph.Validate(run.Workflow),
		"runs":             dashboardView(root)["runs"],
		"run": map[string]any{
			"run_id":  run.RunID,
			"status":  run.State.Status,
			"state":   run.State,
			"metrics": metrics.Snapshot(run, time.Now().UTC()),
		},
	}
	switch run.State.Status {
	case "succeeded", "failed", "cancelled":
		if baseline := previousFinishedRun(run, root); baseline != nil {
			view["comparison"] = metrics.Compare(run, baseline, time.Now().UTC())
		}
	}
	return view
}

func previousFinishedRun(current *model.Run, root string) *model.Run {
	entries, err := os.ReadDir(filepath.Join(root, "runs"))
	if err != nil {
		return nil
	}
	var candidates []*model.Run
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == current.RunID {
			continue
		}
		candidate, err := store.LoadRun(root, entry.Name())
		if err != nil || candidate.State.WorkflowName != current.State.WorkflowName {
			continue
		}
		switch candidate.State.Status {
		case "succeeded", "failed", "cancelled":
			if current.State.CreatedAt == "" || candidate.State.CreatedAt < current.State.CreatedAt {
				candidates = append(candidates, candidate)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].State.CreatedAt > candidates[j].State.CreatedAt })
	if len(candidates) == 0 {
		return nil
	}
	return candidates[0]
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"ok": false, "error": message})
}
