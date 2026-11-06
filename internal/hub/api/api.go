// Package api is the Hub's HTTP JSON interface: the ingest endpoint runners
// report to, and read endpoints for scripts and agents.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
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
	Ingest app.Ingest
	Query  app.Query
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
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "protocol": hubapi.ProtocolVersion})
	})
}

func (a API) reports(w http.ResponseWriter, r *http.Request) {
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
	result, err := a.Ingest.Accept(r.Context(), batch.Reports)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	run, err := a.Query.Run(r.Context(), domain.RunKey{Machine: r.PathValue("machine"), RunID: r.PathValue("run")})
	if errors.Is(err, app.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toRunJSON(run))
}

func (a API) workflows(w http.ResponseWriter, r *http.Request) {
	stats, err := a.Query.Workflows(r.Context(), Days(r, 30))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": toWorkflowsJSON(stats)})
}

func (a API) machines(w http.ResponseWriter, r *http.Request) {
	machines, err := a.Query.Machines(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": toMachinesJSON(machines)})
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
	filter := app.RunFilter{Workflow: query.Get("workflow"), Machine: query.Get("machine"), Project: query.Get("project")}
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
