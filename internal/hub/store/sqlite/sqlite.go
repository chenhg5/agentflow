// Package sqlite stores the Hub in one SQLite file. It uses a pure-Go driver,
// so the agentflow binary stays cgo-free.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// Store is a SQLite-backed app.Store.
type Store struct {
	db *sql.DB
}

var _ app.Store = (*Store)(nil)

// migrations run in order; PRAGMA user_version records how many have run.
var migrations = []string{
	`CREATE TABLE machines (
		id TEXT PRIMARY KEY,
		hostname TEXT NOT NULL DEFAULT '',
		version TEXT NOT NULL DEFAULT '',
		first_seen INTEGER NOT NULL DEFAULT 0,
		last_seen INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE runs (
		machine TEXT NOT NULL,
		run_id TEXT NOT NULL,
		hostname TEXT NOT NULL DEFAULT '',
		project TEXT NOT NULL DEFAULT '',
		path TEXT NOT NULL DEFAULT '',
		version TEXT NOT NULL DEFAULT '',
		orchestration TEXT NOT NULL DEFAULT 'json',
		workflow TEXT NOT NULL,
		digest TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		seq INTEGER NOT NULL,
		created_at INTEGER NOT NULL DEFAULT 0,
		started_at INTEGER NOT NULL DEFAULT 0,
		finished_at INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0,
		received_at INTEGER NOT NULL DEFAULT 0,
		at INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		waiting_ms INTEGER NOT NULL DEFAULT 0,
		trigger_kind TEXT NOT NULL DEFAULT '',
		trigger_id TEXT NOT NULL DEFAULT '',
		inputs TEXT NOT NULL DEFAULT '{}',
		error TEXT NOT NULL DEFAULT '',
		steps TEXT NOT NULL DEFAULT '[]',
		PRIMARY KEY (machine, run_id)
	);
	CREATE INDEX runs_at ON runs (at DESC);
	CREATE INDEX runs_workflow_at ON runs (workflow, at DESC);
	CREATE INDEX runs_status_at ON runs (status, at DESC);`,
}

// Open opens or creates the database at path.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; one connection keeps writes from fighting.
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("hub database is schema %d, this agentflow knows %d: upgrade agentflow", version, len(migrations))
	}
	for index := version; index < len(migrations); index++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[index]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", index+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, index+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const runColumns = `machine, run_id, hostname, project, path, version, orchestration, workflow, digest, status, seq,
	created_at, started_at, finished_at, updated_at, received_at, at, duration_ms, waiting_ms, trigger_kind, trigger_id, inputs, error`

// Save upserts run when its seq is higher than the stored one.
func (s *Store) Save(ctx context.Context, run domain.Run) (bool, error) {
	inputs, err := json.Marshal(nonNilInputs(run.Inputs))
	if err != nil {
		return false, err
	}
	steps, err := json.Marshal(stepRows(run.Steps))
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO runs (`+runColumns+`, steps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (machine, run_id) DO UPDATE SET
			hostname = excluded.hostname, project = excluded.project, path = excluded.path, version = excluded.version,
			orchestration = excluded.orchestration, workflow = excluded.workflow, digest = excluded.digest,
			status = excluded.status, seq = excluded.seq, created_at = excluded.created_at, started_at = excluded.started_at,
			finished_at = excluded.finished_at, updated_at = excluded.updated_at, received_at = excluded.received_at,
			at = excluded.at, duration_ms = excluded.duration_ms, waiting_ms = excluded.waiting_ms,
			trigger_kind = excluded.trigger_kind, trigger_id = excluded.trigger_id, inputs = excluded.inputs,
			error = excluded.error, steps = excluded.steps
		WHERE excluded.seq > runs.seq`,
		run.Key.Machine, run.Key.RunID, run.Hostname, run.Project, run.Path, run.Version, run.Orchestration, run.Workflow,
		run.Digest, string(run.Status), run.Seq, ms(run.CreatedAt), ms(run.StartedAt), ms(run.FinishedAt), ms(run.UpdatedAt),
		ms(run.ReceivedAt), ms(run.When()), run.DurationMs, run.WaitingMs, run.TriggerKind, run.TriggerID, string(inputs),
		run.Error, string(steps))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// Get returns one run with steps.
func (s *Store) Get(ctx context.Context, key domain.RunKey) (domain.Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+runColumns+`, steps FROM runs WHERE machine = ? AND run_id = ?`, key.Machine, key.RunID)
	run, err := scanRun(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, app.ErrNotFound
	}
	return run, err
}

// List returns matching runs newest first and the total match count.
func (s *Store) List(ctx context.Context, filter app.RunFilter) ([]domain.Run, int, error) {
	where, args := whereClause(filter)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	columns := runColumns
	if filter.WithSteps {
		columns += ", steps"
	}
	query := `SELECT ` + columns + ` FROM runs` + where + ` ORDER BY at DESC, machine DESC, run_id DESC`
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d OFFSET %d", filter.Limit, max(filter.Offset, 0))
	} else if filter.Offset > 0 {
		query += fmt.Sprintf(" LIMIT -1 OFFSET %d", filter.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	runs := make([]domain.Run, 0)
	for rows.Next() {
		run, err := scanRun(rows, filter.WithSteps)
		if err != nil {
			return nil, 0, err
		}
		runs = append(runs, run)
	}
	return runs, total, rows.Err()
}

func whereClause(filter app.RunFilter) (string, []any) {
	var clauses []string
	var args []any
	add := func(clause string, values ...any) {
		clauses = append(clauses, clause)
		args = append(args, values...)
	}
	if filter.Workflow != "" {
		add("workflow = ?", filter.Workflow)
	}
	if filter.Machine != "" {
		add("machine = ?", filter.Machine)
	}
	if filter.Project != "" {
		add("project = ?", filter.Project)
	}
	if len(filter.Statuses) > 0 {
		marks := make([]string, len(filter.Statuses))
		for index, status := range filter.Statuses {
			marks[index] = "?"
			args = append(args, string(status))
		}
		clauses = append(clauses, "status IN ("+strings.Join(marks, ", ")+")")
	}
	if !filter.Since.IsZero() {
		add("at >= ?", ms(filter.Since))
	}
	if !filter.Until.IsZero() {
		add("at < ?", ms(filter.Until))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

type scanner interface{ Scan(dest ...any) error }

func scanRun(row scanner, withSteps bool) (domain.Run, error) {
	var run domain.Run
	var status, inputs, steps string
	var created, started, finished, updated, received, at int64
	dest := []any{&run.Key.Machine, &run.Key.RunID, &run.Hostname, &run.Project, &run.Path, &run.Version, &run.Orchestration,
		&run.Workflow, &run.Digest, &status, &run.Seq, &created, &started, &finished, &updated, &received, &at,
		&run.DurationMs, &run.WaitingMs, &run.TriggerKind, &run.TriggerID, &inputs, &run.Error}
	if withSteps {
		dest = append(dest, &steps)
	}
	if err := row.Scan(dest...); err != nil {
		return domain.Run{}, err
	}
	run.Status = domain.Status(status)
	run.CreatedAt, run.StartedAt, run.FinishedAt = fromMs(created), fromMs(started), fromMs(finished)
	run.UpdatedAt, run.ReceivedAt = fromMs(updated), fromMs(received)
	if err := json.Unmarshal([]byte(inputs), &run.Inputs); err != nil {
		return domain.Run{}, fmt.Errorf("run %s inputs: %w", run.Key, err)
	}
	if withSteps {
		var rows []stepRow
		if err := json.Unmarshal([]byte(steps), &rows); err != nil {
			return domain.Run{}, fmt.Errorf("run %s steps: %w", run.Key, err)
		}
		run.Steps = domainSteps(rows)
	}
	return run, nil
}

// GetMachine returns one machine.
func (s *Store) GetMachine(ctx context.Context, id string) (domain.Machine, bool, error) {
	var machine domain.Machine
	var first, last int64
	err := s.db.QueryRowContext(ctx, `SELECT id, hostname, version, first_seen, last_seen FROM machines WHERE id = ?`, id).
		Scan(&machine.ID, &machine.Hostname, &machine.Version, &first, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Machine{}, false, nil
	}
	if err != nil {
		return domain.Machine{}, false, err
	}
	machine.FirstSeen, machine.LastSeen = fromMs(first), fromMs(last)
	return machine, true, nil
}

// SaveMachine upserts a machine.
func (s *Store) SaveMachine(ctx context.Context, machine domain.Machine) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO machines (id, hostname, version, first_seen, last_seen) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET hostname = excluded.hostname, version = excluded.version,
			first_seen = excluded.first_seen, last_seen = excluded.last_seen`,
		machine.ID, machine.Hostname, machine.Version, ms(machine.FirstSeen), ms(machine.LastSeen))
	return err
}

// Machines lists machines by id.
func (s *Store) Machines(ctx context.Context) ([]domain.Machine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, hostname, version, first_seen, last_seen FROM machines ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	machines := make([]domain.Machine, 0)
	for rows.Next() {
		var machine domain.Machine
		var first, last int64
		if err := rows.Scan(&machine.ID, &machine.Hostname, &machine.Version, &first, &last); err != nil {
			return nil, err
		}
		machine.FirstSeen, machine.LastSeen = fromMs(first), fromMs(last)
		machines = append(machines, machine)
	}
	return machines, rows.Err()
}

// stepRow is the stored shape of a step, kept apart from the domain type so
// renaming a domain field never breaks rows already on disk.
type stepRow struct {
	ID          string `json:"id"`
	Type        string `json:"type,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Status      string `json:"status"`
	SkipReason  string `json:"skip_reason,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	FinishedAt  int64  `json:"finished_at,omitempty"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
	WaitingMs   int64  `json:"waiting_ms,omitempty"`
	Error       string `json:"error,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Reason      string `json:"reason,omitempty"`
	OutputBytes int64  `json:"output_bytes,omitempty"`
	LogBytes    int64  `json:"log_bytes,omitempty"`
}

func stepRows(steps []domain.Step) []stepRow {
	rows := make([]stepRow, len(steps))
	for index, step := range steps {
		rows[index] = stepRow{ID: step.ID, Type: step.Type, Provider: step.Provider, Status: string(step.Status),
			SkipReason: step.SkipReason, Attempts: step.Attempts, StartedAt: ms(step.StartedAt), FinishedAt: ms(step.FinishedAt),
			DurationMs: step.DurationMs, WaitingMs: step.WaitingMs, Error: step.Error, Summary: step.Summary, Reason: step.Reason,
			OutputBytes: step.OutputBytes, LogBytes: step.LogBytes}
	}
	return rows
}

func domainSteps(rows []stepRow) []domain.Step {
	steps := make([]domain.Step, len(rows))
	for index, row := range rows {
		steps[index] = domain.Step{ID: row.ID, Type: row.Type, Provider: row.Provider, Status: domain.Status(row.Status),
			SkipReason: row.SkipReason, Attempts: row.Attempts, StartedAt: fromMs(row.StartedAt), FinishedAt: fromMs(row.FinishedAt),
			DurationMs: row.DurationMs, WaitingMs: row.WaitingMs, Error: row.Error, Summary: row.Summary, Reason: row.Reason,
			OutputBytes: row.OutputBytes, LogBytes: row.LogBytes}
	}
	return steps
}

func nonNilInputs(inputs map[string]string) map[string]string {
	if inputs == nil {
		return map[string]string{}
	}
	return inputs
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMs(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
