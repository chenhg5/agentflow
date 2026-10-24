package model

type Workflow struct {
	Version string                     `json:"version"`
	Name    string                     `json:"name"`
	Inputs  map[string]InputDefinition `json:"inputs,omitempty"`
	Nodes   []Node                     `json:"nodes"`
	Entry   string                     `json:"entry,omitempty"`
	Edges   []Transition               `json:"edges,omitempty"`
}

// Transition is a control-flow edge in a version 3 workflow. $end ends a run.
type Transition struct {
	From          string `json:"from"`
	To            string `json:"to"`
	When          string `json:"when,omitempty"`
	MaxIterations int    `json:"maxIterations,omitempty"`
}

type InputDefinition struct {
	Type     string `json:"type,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type Retry struct {
	MaxAttempts int `json:"maxAttempts,omitempty"`
}

type Node struct {
	ID           string         `json:"id"`
	Executor     string         `json:"executor"`
	Config       map[string]any `json:"config,omitempty"`
	InputSchema  map[string]any `json:"inputSchema,omitempty"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Execution    Execution      `json:"execution,omitempty"`
	TimeoutMs    int            `json:"timeoutMs,omitempty"`
	Retry        Retry          `json:"retry,omitempty"`
}

// Execution contains runtime policy that is independent of node data. The
// graph engine owns session selection; executor adapters translate the chosen
// policy into native CLI arguments when they support sessions.
type Execution struct {
	Session SessionSpec `json:"session,omitempty"`
}

// SessionSpec selects how a node obtains an agent session.
//
// new   starts an unrelated session for every visit.
// fixed creates one session per run and resumes it on later visits, or resumes
// the configured ID when ID is provided.
// inherit resumes the incoming node's session, or the session from From.
// fork starts a new session from the incoming or From node's session.
type SessionSpec struct {
	Mode string `json:"mode,omitempty"`
	ID   string `json:"id,omitempty"`
	From string `json:"from,omitempty"`
}

type NodeState struct {
	Status            string            `json:"status"`
	IncomingNode      string            `json:"incomingNode,omitempty"`
	IncomingSessionID string            `json:"incomingSessionId,omitempty"`
	Workspace         string            `json:"workspace,omitempty"`
	SessionID         string            `json:"sessionId,omitempty"`
	Reason            string            `json:"reason,omitempty"`
	Attempts          int               `json:"attempts,omitempty"`
	DurationMs        int64             `json:"durationMs,omitempty"`
	WaitDurationMs    int64             `json:"waitDurationMs,omitempty"`
	InputBytes        int64             `json:"inputBytes,omitempty"`
	OutputBytes       int64             `json:"outputBytes,omitempty"`
	StdoutBytes       int64             `json:"stdoutBytes,omitempty"`
	StderrBytes       int64             `json:"stderrBytes,omitempty"`
	Error             string            `json:"error,omitempty"`
	StartedAt         string            `json:"startedAt,omitempty"`
	FinishedAt        string            `json:"finishedAt,omitempty"`
	Result            any               `json:"result,omitempty"`
	Input             any               `json:"input,omitempty"`
	Artifacts         map[string]string `json:"artifacts,omitempty"`
	LeaseToken        string            `json:"leaseToken,omitempty"`
}

type RunState struct {
	RunID          string                `json:"runId"`
	WorkflowName   string                `json:"workflowName"`
	WorkflowDigest string                `json:"workflowDigest,omitempty"`
	Status         string                `json:"status"`
	Inputs         map[string]string     `json:"inputs"`
	IdempotencyKey string                `json:"idempotency_key,omitempty"`
	Nodes          map[string]*NodeState `json:"nodes"`
	ActiveNode     string                `json:"activeNode,omitempty"`
	Outcome        string                `json:"outcome,omitempty"`
	Visits         map[string]int        `json:"visits,omitempty"`
	Traversals     map[string]int        `json:"traversals,omitempty"`
	History        []NodeVisit           `json:"history,omitempty"`
	CreatedAt      string                `json:"createdAt"`
	UpdatedAt      string                `json:"updatedAt"`
	StartedAt      string                `json:"startedAt,omitempty"`
	FinishedAt     string                `json:"finishedAt,omitempty"`
	DurationMs     int64                 `json:"durationMs,omitempty"`
	Trigger        *TriggerInfo          `json:"trigger,omitempty"`
	Evaluation     *Evaluation           `json:"evaluation,omitempty"`
}

// Evaluation stores measurements produced by a human, program, or agent after
// a run. Measurement names and units are user-defined; AgentFlow only uses the
// numeric value and direction when comparing runs.
type Evaluation struct {
	Source       string                 `json:"source,omitempty"`
	EvaluatedAt  string                 `json:"evaluatedAt"`
	Measurements map[string]Measurement `json:"measurements,omitempty"`
	Labels       map[string]string      `json:"labels,omitempty"`
	Notes        string                 `json:"notes,omitempty"`
}

type Measurement struct {
	Value     float64 `json:"value"`
	Unit      string  `json:"unit,omitempty"`
	Direction string  `json:"direction,omitempty"`
}

type NodeVisit struct {
	NodeID string    `json:"nodeId"`
	Visit  int       `json:"visit"`
	State  NodeState `json:"state"`
}

type TriggerInfo struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	DeliveryID  string `json:"deliveryId,omitempty"`
	ScheduledAt string `json:"scheduledAt,omitempty"`
	ReceivedAt  string `json:"receivedAt,omitempty"`
}

// Control is a request for a run's executor. Commands that change a running
// workflow enqueue a control instead of
// writing run state, so the executor stays the only writer.
type Control struct {
	Kind   string         `json:"kind"`
	NodeID string         `json:"nodeId,omitempty"`
	Nodes  []string       `json:"nodes,omitempty"`
	Output map[string]any `json:"output,omitempty"`
	At     string         `json:"at"`
}

type Run struct {
	RunID    string
	RunDir   string
	Workflow Workflow
	State    RunState
}
