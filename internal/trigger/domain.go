package trigger

import (
	"context"
	"encoding/json"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

type Config struct {
	Triggers []Definition `json:"triggers"`
}

type Definition struct {
	ID       string             `json:"id"`
	Enabled  bool               `json:"enabled"`
	Source   SourceConfig       `json:"source"`
	Target   TargetConfig       `json:"target"`
	Inputs   map[string]string  `json:"inputs,omitempty"`
	Bindings map[string]Binding `json:"bindings,omitempty"`
	Policy   Policy             `json:"policy,omitempty"`
}

type SourceConfig struct {
	Kind             string      `json:"kind"`
	Expression       string      `json:"expression,omitempty"`
	Timezone         string      `json:"timezone,omitempty"`
	Path             string      `json:"path,omitempty"`
	Method           string      `json:"method,omitempty"`
	Auth             *AuthConfig `json:"auth,omitempty"`
	DeliveryIDHeader string      `json:"deliveryIDHeader,omitempty"`
	SignatureHeader  string      `json:"signatureHeader,omitempty"`
	MaxBodyBytes     int64       `json:"maxBodyBytes,omitempty"`
}

type AuthConfig struct {
	Kind      string `json:"kind"`
	SecretEnv string `json:"secretEnv,omitempty"`
	TokenEnv  string `json:"tokenEnv,omitempty"`
}

type TargetConfig struct {
	Workflow string `json:"workflow"`
}

type Binding struct {
	From string `json:"from"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

type Policy struct {
	Concurrency string `json:"concurrency,omitempty"`
	Misfire     string `json:"misfire,omitempty"`
}

type Event struct {
	TriggerID   string            `json:"trigger_id"`
	SourceKind  string            `json:"source_kind"`
	DeliveryID  string            `json:"delivery_id"`
	ReceivedAt  time.Time         `json:"received_at"`
	ScheduledAt *time.Time        `json:"scheduled_at,omitempty"`
	Payload     json.RawMessage   `json:"payload,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
	Timezone    string            `json:"schedule_timezone,omitempty"`
	CatchUp     bool              `json:"catch_up,omitempty"`
	Manual      bool              `json:"manual,omitempty"`
}

type StartRunCommand struct {
	WorkflowPath   string            `json:"workflow_path"`
	Inputs         map[string]string `json:"inputs"`
	TriggerID      string            `json:"trigger_id"`
	TriggerKind    string            `json:"trigger_kind"`
	DeliveryID     string            `json:"delivery_id"`
	ScheduledAt    *time.Time        `json:"scheduled_at,omitempty"`
	ReceivedAt     time.Time         `json:"received_at"`
	IdempotencyKey string            `json:"idempotency_key"`
	Timezone       string            `json:"schedule_timezone,omitempty"`
	CatchUp        bool              `json:"catch_up,omitempty"`
	Workflow       *model.Workflow   `json:"workflow_snapshot,omitempty"`
}

type RunRef struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

type RunStarter interface {
	Prepare(context.Context, StartRunCommand) (StartRunCommand, error)
	Start(context.Context, StartRunCommand) (RunRef, error)
	ValidateReplay(context.Context, StartRunCommand, RunRef) error
}

type DispatchResult struct {
	TriggerID  string `json:"trigger_id"`
	DeliveryID string `json:"delivery_id"`
	Status     string `json:"status"`
	Run        RunRef `json:"run"`
	Duplicate  bool   `json:"duplicate,omitempty"`
	Reason     string `json:"reason,omitempty"`
}
