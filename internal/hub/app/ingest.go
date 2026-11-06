package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/domain"
	"github.com/chenhg5/agentflow/internal/hubapi"
)

// Ingest turns reports into stored runs. A report older than the stored run is
// counted as stale and ignored; an invalid report is rejected without stopping
// the rest of the batch.
type Ingest struct {
	Store Store
	Now   func() time.Time
}

// Accept stores a batch of reports.
func (s Ingest) Accept(ctx context.Context, reports []hubapi.RunReport) (hubapi.IngestResult, error) {
	var result hubapi.IngestResult
	now := s.now()
	for _, report := range reports {
		run, err := runFromReport(report, now)
		if err == nil {
			err = run.Validate()
		}
		if err != nil {
			result.Rejected = append(result.Rejected, fmt.Sprintf("%s: %v", report.RunID, err))
			continue
		}
		saved, err := s.Store.Save(ctx, run)
		if err != nil {
			return result, err
		}
		if !saved {
			result.Stale++
			continue
		}
		result.Accepted++
		if err := s.seeMachine(ctx, report.Source, now); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s Ingest) seeMachine(ctx context.Context, source hubapi.Source, at time.Time) error {
	machine, _, err := s.Store.GetMachine(ctx, source.Machine)
	if err != nil {
		return err
	}
	machine.ID = source.Machine
	return s.Store.SaveMachine(ctx, machine.Seen(source.Hostname, source.Version, at))
}

func (s Ingest) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// runFromReport translates the wire contract into the domain model.
func runFromReport(report hubapi.RunReport, receivedAt time.Time) (domain.Run, error) {
	if report.Protocol != hubapi.ProtocolVersion {
		return domain.Run{}, errors.Join(domain.ErrInvalid, fmt.Errorf("unsupported protocol %d", report.Protocol))
	}
	orchestration := report.Orchestration
	if orchestration == "" {
		orchestration = hubapi.OrchestrationJSON
	}
	run := domain.Run{
		Key:           domain.RunKey{Machine: report.Source.Machine, RunID: report.RunID},
		Hostname:      report.Source.Hostname,
		Project:       report.Source.Project,
		Path:          report.Source.Path,
		Version:       report.Source.Version,
		Orchestration: orchestration,
		Workflow:      report.Workflow,
		Digest:        report.WorkflowDigest,
		Status:        domain.Status(report.Status),
		Seq:           report.Seq,
		CreatedAt:     parseTime(report.CreatedAt),
		StartedAt:     parseTime(report.StartedAt),
		FinishedAt:    parseTime(report.FinishedAt),
		UpdatedAt:     parseTime(report.UpdatedAt),
		ReceivedAt:    receivedAt,
		DurationMs:    report.DurationMs,
		WaitingMs:     report.WaitingMs,
		Inputs:        report.Inputs,
		Error:         report.Error,
		Steps:         make([]domain.Step, 0, len(report.Steps)),
	}
	if report.Trigger != nil {
		run.TriggerKind, run.TriggerID = report.Trigger.Kind, report.Trigger.ID
	}
	for _, step := range report.Steps {
		run.Steps = append(run.Steps, domain.Step{
			ID: step.ID, Type: step.Type, Provider: step.Provider, Status: domain.Status(step.Status), SkipReason: step.SkipReason,
			Attempts: step.Attempts, StartedAt: parseTime(step.StartedAt), FinishedAt: parseTime(step.FinishedAt),
			DurationMs: step.DurationMs, WaitingMs: step.WaitingMs, Error: step.Error, Summary: step.Summary, Reason: step.Reason,
			OutputBytes: step.OutputBytes, LogBytes: step.LogBytes,
		})
	}
	return run, nil
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
