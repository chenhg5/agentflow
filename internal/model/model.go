package model

type Workflow struct {
	Version     string                     `json:"version"`
	Name        string                     `json:"name"`
	Inputs      map[string]InputDefinition `json:"inputs,omitempty"`
	MaxParallel int                        `json:"maxParallel,omitempty"`
	Nodes       []Node                     `json:"nodes"`
}

type InputDefinition struct {
	Type     string `json:"type,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type Retry struct {
	MaxAttempts int `json:"maxAttempts,omitempty"`
}

type ResultSpec struct {
	Path     string `json:"path,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type FieldSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type Node struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Provider     string            `json:"provider,omitempty"`
	Command      []string          `json:"command,omitempty"`
	Prompt       string            `json:"prompt,omitempty"`
	PromptMode   string            `json:"promptMode,omitempty"`
	Workspace    string            `json:"workspace,omitempty"`
	Session      string            `json:"session,omitempty"`
	Input        map[string]any    `json:"input,omitempty"`
	InputSchema  map[string]any    `json:"inputSchema,omitempty"`
	OutputSchema map[string]any    `json:"outputSchema,omitempty"`
	InputFields  []FieldSpec       `json:"inputFields,omitempty"`
	OutputFields []FieldSpec       `json:"outputFields,omitempty"`
	InputMapping map[string]string `json:"inputMapping,omitempty"`
	Needs        []string          `json:"needs,omitempty"`
	When         string            `json:"when,omitempty"`
	RunIf        string            `json:"runIf,omitempty"`
	TimeoutMs    int               `json:"timeoutMs,omitempty"`
	Retry        Retry             `json:"retry,omitempty"`
	Result       *ResultSpec       `json:"result,omitempty"`
}

type NodeState struct {
	Status         string            `json:"status"`
	Prompt         string            `json:"prompt,omitempty"`
	Workspace      string            `json:"workspace,omitempty"`
	SessionID      string            `json:"sessionId,omitempty"`
	Reason         string            `json:"reason,omitempty"`
	Attempts       int               `json:"attempts,omitempty"`
	DurationMs     int64             `json:"durationMs,omitempty"`
	WaitDurationMs int64             `json:"waitDurationMs,omitempty"`
	InputBytes     int64             `json:"inputBytes,omitempty"`
	OutputBytes    int64             `json:"outputBytes,omitempty"`
	StdoutBytes    int64             `json:"stdoutBytes,omitempty"`
	StderrBytes    int64             `json:"stderrBytes,omitempty"`
	Error          string            `json:"error,omitempty"`
	StartedAt      string            `json:"startedAt,omitempty"`
	FinishedAt     string            `json:"finishedAt,omitempty"`
	Result         any               `json:"result,omitempty"`
	Input          any               `json:"input,omitempty"`
	Artifacts      map[string]string `json:"artifacts,omitempty"`
	LeaseToken     string            `json:"leaseToken,omitempty"`
}

type RunState struct {
	RunID          string                `json:"runId"`
	WorkflowName   string                `json:"workflowName"`
	WorkflowDigest string                `json:"workflowDigest,omitempty"`
	Status         string                `json:"status"`
	Inputs         map[string]string     `json:"inputs"`
	IdempotencyKey string                `json:"idempotency_key,omitempty"`
	Nodes          map[string]*NodeState `json:"nodes"`
	CreatedAt      string                `json:"createdAt"`
	UpdatedAt      string                `json:"updatedAt"`
	StartedAt      string                `json:"startedAt,omitempty"`
	FinishedAt     string                `json:"finishedAt,omitempty"`
	DurationMs     int64                 `json:"durationMs,omitempty"`
	Trigger        *TriggerInfo          `json:"trigger,omitempty"`
}

type TriggerInfo struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	DeliveryID  string `json:"deliveryId,omitempty"`
	ScheduledAt string `json:"scheduledAt,omitempty"`
	ReceivedAt  string `json:"receivedAt,omitempty"`
}

// Control is a request for a run's executor. Commands that change a running
// workflow (approve, reject, cancel, retry) enqueue a control instead of
// writing run state, so the executor stays the only writer.
type Control struct {
	Kind    string   `json:"kind"`
	NodeID  string   `json:"nodeId,omitempty"`
	Nodes   []string `json:"nodes,omitempty"`
	Message string   `json:"message,omitempty"`
	At      string   `json:"at"`
}

type Run struct {
	RunID    string
	RunDir   string
	Workflow Workflow
	State    RunState
}
