// Package api is the Hub's HTTP JSON interface: the ingest endpoint runners
// report to, and read endpoints for scripts and agents.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
	"github.com/chenhg5/agentflow/internal/hubapi"
)

// maxBody bounds one ingest request.
const maxBody = 16 << 20

// API serves the JSON endpoints.
type API struct {
	Ingest   app.Ingest
	Query    app.Query
	Commands app.Commands
}

// Register mounts the endpoints. ingest guards the report endpoint and view
// guards the read endpoints.
func (a API) Register(mux *http.ServeMux, ingest, view Guard) {
	mux.Handle("POST "+hubapi.IngestPath, ingest(http.HandlerFunc(a.reports)))
	mux.Handle("GET /api/v1/overview", view(http.HandlerFunc(a.overview)))
	mux.Handle("GET /api/v1/runs", view(http.HandlerFunc(a.runs)))
	mux.Handle("GET /api/v1/runs/{machine}/{run}", view(http.HandlerFunc(a.run)))
	mux.Handle("GET /api/v1/workflows", view(http.HandlerFunc(a.workflows)))
	mux.Handle("GET /api/v1/machines", view(http.HandlerFunc(a.machines)))
	mux.Handle("GET /api/v1/steps", view(http.HandlerFunc(a.steps)))
	mux.Handle("GET /api/v1/workflows/{name}", view(http.HandlerFunc(a.workflow)))
	// Runners pull commands and answer them with the ingest token.
	mux.Handle("GET "+hubapi.CommandsPath, ingest(http.HandlerFunc(a.pendingCommands)))
	mux.Handle("POST "+hubapi.CommandsPath+"/{id}", ingest(http.HandlerFunc(a.answerCommand)))
	// People submit input with view access, from the same origin only.
	mux.Handle("POST /api/v1/runs/{machine}/{run}/submit", view(SameOrigin(http.HandlerFunc(a.submit))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "protocol": hubapi.ProtocolVersion})
	})
}

func (a API) reports(w http.ResponseWriter, r *http.Request) {
	// Only JSON: a web page cannot send that cross-origin without a CORS
	// preflight, which the Hub never grants, so pages a user visits cannot
	// post fake runs to a Hub on their machine.
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var batch hubapi.Batch
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := decoder.Decode(&batch); err != nil {
		writeError(w, http.StatusBadRequest, "body must be a report batch: "+err.Error())
		return
	}
	if len(batch.Reports) > hubapi.MaxBatch {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("at most %d reports per batch", hubapi.MaxBatch))
		return
	}
	var result hubapi.IngestResult
	runs := make([]domain.Run, 0, len(batch.Reports))
	for _, report := range batch.Reports {
		run, err := runFromReport(report)
		if err != nil {
			result.Rejected = append(result.Rejected, fmt.Sprintf("%s: %v", report.RunID, err))
			continue
		}
		runs = append(runs, run)
	}
	accepted, err := a.Ingest.Accept(r.Context(), runs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result.Accepted, result.Stale = accepted.Accepted, accepted.Stale
	result.Rejected = append(result.Rejected, accepted.Rejected...)
	writeJSON(w, http.StatusOK, result)
}

func (a API) overview(w http.ResponseWriter, r *http.Request) {
	overview, err := a.Query.Overview(r.Context(), Days(r, 14))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toOverviewJSON(overview))
}

func (a API) runs(w http.ResponseWriter, r *http.Request) {
	filter, err := Filter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runs, total, err := a.Query.Runs(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": toRunsJSON(runs), "total": total})
}

func (a API) run(w http.ResponseWriter, r *http.Request) {
	detail, err := a.Query.Detail(r.Context(), domain.RunKey{Machine: r.PathValue("machine"), RunID: r.PathValue("run")})
	if errors.Is(err, app.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toRunDetailJSON(detail))
}

func (a API) workflows(w http.ResponseWriter, r *http.Request) {
	stats, err := a.Query.Workflows(r.Context(), Days(r, 30), Tags(r), strings.TrimSpace(r.URL.Query().Get("group")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": toWorkflowsJSON(stats), "tags": app.Tags(stats), "groups": groupNames(stats)})
}

func (a API) machines(w http.ResponseWriter, r *http.Request) {
	machines, err := a.Query.Machines(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": toMachinesJSON(machines)})
}

func (a API) workflow(w http.ResponseWriter, r *http.Request) {
	days := Days(r, 30)
	detail, err := a.Query.Workflow(r.Context(), r.PathValue("name"), days)
	if errors.Is(err, app.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no runs of this workflow")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toWorkflowDetailJSON(detail))
}

func (a API) steps(w http.ResponseWriter, r *http.Request) {
	days := Days(r, 30)
	stats, err := a.Query.Steps(r.Context(), r.URL.Query().Get("workflow"), days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "steps": toStepStatsJSON(stats)})
}

// Tags reads ?tag= (repeatable, or comma-separated): runs must carry every one.
func Tags(r *http.Request) []string {
	var tags []string
	for _, value := range r.URL.Query()["tag"] {
		for _, tag := range strings.Split(value, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				tags = append(tags, tag)
			}
		}
	}
	return tags
}

func groupNames(stats []app.WorkflowStat) []string {
	names := []string{}
	for _, group := range app.GroupWorkflows(stats) {
		if group.Name != "" {
			names = append(names, group.Name)
		}
	}
	return names
}

// Days reads ?days=, clamped to 1..90.
func Days(r *http.Request, fallback int) int {
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 1 {
		return fallback
	}
	return min(days, 90)
}

// Filter reads a run filter from the query string. The pages use it too.
func Filter(r *http.Request) (app.RunFilter, error) {
	query := r.URL.Query()
	filter := app.RunFilter{Workflow: query.Get("workflow"), Machine: query.Get("machine"), Project: query.Get("project"), RunID: query.Get("run"),
		Tags: Tags(r), Group: strings.TrimSpace(query.Get("group"))}
	for _, value := range strings.Split(query.Get("status"), ",") {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		status := domain.Status(value)
		if !status.Known() {
			return filter, fmt.Errorf("unknown status %q", value)
		}
		filter.Statuses = append(filter.Statuses, status)
	}
	var err error
	if filter.Limit, err = intParam(query.Get("limit")); err != nil {
		return filter, fmt.Errorf("limit: %w", err)
	}
	if filter.Offset, err = intParam(query.Get("offset")); err != nil {
		return filter, fmt.Errorf("offset: %w", err)
	}
	if filter.Since, err = timeParam(query.Get("since")); err != nil {
		return filter, fmt.Errorf("since: %w", err)
	}
	if filter.Until, err = timeParam(query.Get("until")); err != nil {
		return filter, fmt.Errorf("until: %w", err)
	}
	return filter, nil
}

func intParam(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, errors.New("must be a non-negative integer")
	}
	return parsed, nil
}

func timeParam(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	parsed, err := time.ParseInLocation(time.DateOnly, value, time.Local)
	if err != nil {
		return time.Time{}, errors.New("use RFC 3339 or YYYY-MM-DD")
	}
	return parsed, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func (a API) pendingCommands(w http.ResponseWriter, r *http.Request) {
	machine := r.URL.Query().Get("machine")
	if machine == "" {
		writeError(w, http.StatusBadRequest, "machine is required")
		return
	}
	commands, err := a.Commands.Pending(r.Context(), machine)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list := hubapi.CommandList{Commands: make([]hubapi.Command, 0, len(commands))}
	for _, command := range commands {
		list.Commands = append(list.Commands, toWireCommand(command))
	}
	writeJSON(w, http.StatusOK, list)
}

func (a API) answerCommand(w http.ResponseWriter, r *http.Request) {
	var answer struct {
		hubapi.CommandResult
		Machine string `json:"machine"`
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&answer); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	machine := r.URL.Query().Get("machine")
	if answer.Machine != "" {
		machine = answer.Machine
	}
	err := a.Commands.Answer(r.Context(), r.PathValue("id"), machine, domain.CommandStatus(answer.Status), answer.Error)
	switch {
	case errors.Is(err, app.ErrNotFound):
		writeError(w, http.StatusNotFound, "command not found for this machine")
	case errors.Is(err, app.ErrCommandDone):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (a API) submit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NodeID string         `json:"node_id"`
		Output map[string]any `json:"output"`
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	command, err := a.Commands.Submit(r.Context(), domain.RunKey{Machine: r.PathValue("machine"), RunID: r.PathValue("run")}, body.NodeID, body.Output, Who(r))
	if err != nil {
		writeError(w, SubmitStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "command": toWireCommand(command)})
}

// SubmitStatus maps a submission error to an HTTP status.
func SubmitStatus(err error) int {
	switch {
	case errors.Is(err, app.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, domain.ErrNotWaiting), errors.Is(err, domain.ErrNoCapability):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// Who names the person behind a request: the basic-auth user when there is
// one, else "web".
func Who(r *http.Request) string {
	if user, _, ok := r.BasicAuth(); ok && user != "" {
		return user
	}
	return "web"
}

func toWireCommand(command domain.Command) hubapi.Command {
	wire := hubapi.Command{ID: command.ID, Kind: command.Kind, RunID: command.Run.RunID, NodeID: command.NodeID, Output: command.Output,
		By: command.By, CreatedAt: command.CreatedAt.Format(time.RFC3339Nano)}
	if !command.StepStartedAt.IsZero() {
		wire.StepStartedAt = command.StepStartedAt.Format(time.RFC3339Nano)
	}
	return wire
}
