package trigger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chenhg5/agentflow/internal/store"
	"github.com/robfig/cron/v3"
)

// Private deterministic process-test boundary; production never reads an env flag.
var triggerCheckpoint func(string)

func triggerBoundary(stage string) {
	if triggerCheckpoint != nil {
		triggerCheckpoint(stage)
	}
}

func OccurrenceID(triggerID string, planned time.Time) string {
	return triggerID + ":" + planned.UTC().Format(time.RFC3339Nano)
}

func cronSchedule(definition Definition) (cron.Schedule, error) {
	zone := definition.Source.Timezone
	if zone == "" {
		zone = "UTC"
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	return parser.Parse("CRON_TZ=" + zone + " " + definition.Source.Expression)
}

type CronSkip struct {
	Count  int       `json:"count"`
	First  time.Time `json:"first"`
	Last   time.Time `json:"last"`
	Reason string    `json:"reason"`
}

type CronBaseline struct {
	At              time.Time `json:"at"`
	PreviousThrough time.Time `json:"previous_through"`
	Reason          string    `json:"reason"`
	Fingerprint     string    `json:"fingerprint"`
}

type CronPlan struct {
	Through    time.Time  `json:"through"`
	Definition Definition `json:"definition"`
	Event      *Event     `json:"event,omitempty"`
	Skip       *CronSkip  `json:"skip,omitempty"`
}

type CronCursor struct {
	TriggerID   string          `json:"trigger_id"`
	Fingerprint string          `json:"fingerprint"`
	Through     time.Time       `json:"through"`
	Enabled     bool            `json:"enabled"`
	Pending     *CronPlan       `json:"pending,omitempty"`
	Baselines   []CronBaseline  `json:"baselines"`
	Skipped     []CronSkip      `json:"skipped"`
	LastResult  *DispatchResult `json:"last_result,omitempty"`
}

func cronCursorPath(root, id string) string {
	hash := sha256.Sum256([]byte(id))
	return filepath.Join(root, "trigger-cursors", hex.EncodeToString(hash[:])+".json")
}

func CronStatus(root string) ([]CronCursor, error) {
	paths, err := filepath.Glob(filepath.Join(root, "trigger-cursors", "*.json"))
	if err != nil {
		return nil, err
	}
	out := []CronCursor{}
	for _, path := range paths {
		var cursor CronCursor
		if err := store.ReadAtomicJSON(path, &cursor); err != nil {
			return nil, err
		}
		out = append(out, cursor)
	}
	return out, nil
}

type CronRunner struct {
	Dispatcher *Dispatcher
	Now        func() time.Time
}

// Tick is clock-injectable; startup distinguishes downtime from a live timer.
// A complete durable plan is published before any receipt/run/watermark changes.
func (r *CronRunner) Tick(ctx context.Context, startup bool) error {
	d := r.Dispatcher
	release, err := acquireWorkspaceLock(ctx, d.Root, "cron-state.lock", true)
	if err != nil {
		return err
	}
	defer release()
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	for _, definition := range d.Config.Triggers {
		path := cronCursorPath(d.Root, definition.ID)
		var cursor CronCursor
		err := store.ReadAtomicJSON(path, &cursor)
		missing := os.IsNotExist(err)
		if err != nil && !missing {
			return err
		}
		if !missing && (cursor.TriggerID != definition.ID || cursor.Through.IsZero() || cursor.Fingerprint == "") {
			return fmt.Errorf("invalid cron cursor for %s", definition.ID)
		}
		if cursor.Pending != nil {
			if err := r.finishPlan(ctx, &cursor, path); err != nil {
				return err
			}
		}
		if definition.Source.Kind != "cron" && missing {
			continue
		}
		data, _ := json.Marshal(definition)
		hash := sha256.Sum256(data)
		fingerprint := hex.EncodeToString(hash[:])
		enabled := definition.Enabled && definition.Source.Kind == "cron"
		if missing || cursor.Fingerprint != fingerprint {
			reason := "definition_changed"
			if missing {
				reason = "first_enable"
			} else if !enabled {
				reason = "disabled"
			} else if !cursor.Enabled {
				reason = "reenabled"
			}
			cursor.TriggerID = definition.ID
			cursor.Baselines = append(cursor.Baselines, CronBaseline{At: now, PreviousThrough: cursor.Through, Reason: reason, Fingerprint: fingerprint})
			cursor.Fingerprint, cursor.Through, cursor.Enabled = fingerprint, now, enabled
			if err := store.WriteAtomicJSON(path, cursor); err != nil {
				return err
			}
			continue // First enable/edit never invents historical occurrences.
		}
		if !enabled || now.Before(cursor.Through) {
			continue
		}
		if definition.Policy.Misfire != "" && definition.Policy.Misfire != "skip" && definition.Policy.Misfire != "fire-once" {
			return fmt.Errorf("unsupported misfire policy %s", definition.Policy.Misfire)
		}
		schedule, err := cronSchedule(definition)
		if err != nil {
			return err
		}
		var first, latest, previous time.Time
		count := 0
		for planned := schedule.Next(cursor.Through); !planned.IsZero() && !planned.After(now); planned = schedule.Next(planned) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if count == 0 {
				first = planned
			}
			previous, latest = latest, planned
			count++
		}
		if count == 0 {
			continue
		}
		misfire := startup || now.Sub(latest) >= time.Minute
		fire := !misfire || definition.Policy.Misfire == "fire-once"
		plan := CronPlan{Through: now, Definition: definition}
		skipped := count
		if fire {
			zone := definition.Source.Timezone
			if zone == "" {
				zone = "UTC"
			}
			plan.Event = &Event{TriggerID: definition.ID, SourceKind: "cron", DeliveryID: OccurrenceID(definition.ID, latest), ScheduledAt: &latest, ReceivedAt: now, Timezone: zone, CatchUp: misfire}
			skipped--
		}
		if skipped > 0 {
			last := latest
			if fire {
				last = previous
			}
			plan.Skip = &CronSkip{Count: skipped, First: first, Last: last, Reason: "misfire"}
		}
		cursor.Pending = &plan
		if err := store.WriteAtomicJSON(path, cursor); err != nil {
			return err
		}
		triggerBoundary("plan_persisted")
		if err := r.finishPlan(ctx, &cursor, path); err != nil {
			return err
		}
	}
	return nil
}

func (r *CronRunner) finishPlan(ctx context.Context, cursor *CronCursor, path string) error {
	plan := cursor.Pending
	if plan == nil || plan.Definition.ID != cursor.TriggerID || plan.Through.Before(cursor.Through) {
		return fmt.Errorf("invalid pending cron plan")
	}
	if plan.Event != nil {
		// A persisted plan belongs to its frozen definition, even if edited.
		dispatcher := &Dispatcher{Root: r.Dispatcher.Root, Config: Config{Triggers: []Definition{plan.Definition}}, Starter: r.Dispatcher.Starter}
		result, err := dispatcher.Dispatch(ctx, *plan.Event)
		if err != nil {
			return err
		}
		cursor.LastResult = &result
	}
	if plan.Skip != nil {
		cursor.Skipped = append(cursor.Skipped, *plan.Skip)
	}
	cursor.Through, cursor.Pending = plan.Through, nil
	if err := store.WriteAtomicJSON(path, cursor); err != nil {
		return err
	}
	triggerBoundary("cursor_advanced")
	return nil
}
