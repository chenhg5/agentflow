package trigger

import (
	"context"
	"encoding/json"
	"time"
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
	TriggerID   string
	SourceKind  string
	DeliveryID  string
	ReceivedAt  time.Time
	ScheduledAt *time.Time
	Payload     json.RawMessage
	Headers     map[string]string
	Attributes  map[string]string
}

type StartRunCommand struct {
	WorkflowPath   string
	Inputs         map[string]string
	TriggerID      string
	TriggerKind    string
	DeliveryID     string
	ScheduledAt    *time.Time
	ReceivedAt     time.Time
	IdempotencyKey string
}

type RunRef struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

type RunStarter interface {
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
