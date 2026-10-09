package model

type Workflow struct {
	Version string                     `json:"version"`
	Name    string                     `json:"name"`
	Inputs  map[string]InputDefinition `json:"inputs,omitempty"`
	Nodes   []Node                     `json:"nodes"`
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

type Node struct {
	ID         string      `json:"id"`
	Type       string      `json:"type"`
	Driver     string      `json:"driver,omitempty"`
	Command    []string    `json:"command,omitempty"`
	Prompt     string      `json:"prompt,omitempty"`
	PromptMode string      `json:"promptMode,omitempty"`
	Workspace  string      `json:"workspace,omitempty"`
	Needs      []string    `json:"needs,omitempty"`
	When       string      `json:"when,omitempty"`
	TimeoutMs  int         `json:"timeoutMs,omitempty"`
	Retry      Retry       `json:"retry,omitempty"`
	Result     *ResultSpec `json:"result,omitempty"`
}

type NodeState struct {
	Status     string            `json:"status"`
	Prompt     string            `json:"prompt,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Error      string            `json:"error,omitempty"`
	StartedAt  string            `json:"startedAt,omitempty"`
	FinishedAt string            `json:"finishedAt,omitempty"`
	Result     any               `json:"result,omitempty"`
	Artifacts  map[string]string `json:"artifacts,omitempty"`
	LeaseToken string            `json:"leaseToken,omitempty"`
}

type RunState struct {
	RunID          string                `json:"runId"`
	WorkflowName   string                `json:"workflowName"`
	Status         string                `json:"status"`
	Inputs         map[string]string     `json:"inputs"`
	IdempotencyKey string                `json:"idempotency_key,omitempty"`
	Nodes          map[string]*NodeState `json:"nodes"`
	CreatedAt      string                `json:"createdAt"`
	UpdatedAt      string                `json:"updatedAt"`
}

type Run struct {
	RunID    string
	RunDir   string
	Workflow Workflow
	State    RunState
}
