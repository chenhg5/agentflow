package model

type Workflow struct {
	Version string                     `json:"version"`
	Name    string                     `json:"name"`
	Inputs  map[string]InputDefinition `json:"inputs,omitempty"`
	Nodes   []Node                     `json:"nodes"`
	Entry   string                     `json:"entry,omitempty"`
	Edges   []Transition               `json:"edges,omitempty"`
	// AllowUnboundedCycles explicitly permits cycles with no maxIterations edge.
	// The default remains bounded to catch accidental infinite loops.
	AllowUnboundedCycles bool `json:"allowUnboundedCycles,omitempty"`
}

// Transition is a control-flow edge in a version 3 workflow. $end ends a run.
type Transition struct {
	From          string `json:"from"`
	To            string `json:"to"`
	When          string `json:"when,omitempty"`
	MaxIterations int    `json:"maxIterations,omitempty"`
	// Map optionally builds the target input from the source input, output,
	// run inputs, and node results. Without it, the source output is forwarded.
	Map *map[string]any `json:"map,omitempty"`
}

type InputDefinition struct {
	Type     string `json:"type,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type Retry struct {
	MaxAttempts int `json:"maxAttempts,omitempty"`
}

type ResourceRequest struct {
	Key   string `json:"key"`
	Units int    `json:"units"`
}

type Node struct {
	Resources []ResourceRequest `json:"resources,omitempty"`
	ID        string            `json:"id"`
	// Dispatch selects one matching outgoing edge by default, or all matching
	// edges when set to "all".
	Dispatch string `json:"dispatch,omitempty"`
	// Join waits for one payload from every named immediate predecessor.
	Join         *JoinSpec      `json:"join,omitempty"`
	InputSchema  map[string]any `json:"inputSchema,omitempty"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Execution    Execution      `json:"execution,omitempty"`
	TimeoutMs    int            `json:"timeoutMs,omitempty"`
	Retry        Retry          `json:"retry,omitempty"`
}

type JoinSpec struct {
	From []string `json:"from"`
}

// Execution contains the ordered ways a logical node may be executed. A
// candidate failure stays inside the logical node; graph edges only observe
// the first successful candidate result.
type Execution struct {
	Candidates []ExecutorCandidate `json:"candidates"`
	Fallback   *FallbackSpec       `json:"fallback,omitempty"`
}

type ExecutorCandidate struct {
	Executor string         `json:"executor"`
	Config   map[string]any `json:"config,omitempty"`
	Session  SessionSpec    `json:"session,omitempty"`
}

type FallbackSpec struct {
	When string `json:"when,omitempty"`
}

// NodeContinuation is the durable decision taken when an attempt completes
// unsuccessfully. It is persisted together with the completed attempt so a
// restart resumes the recorded retry/fallback/terminal outcome instead of
// replaying an attempt that already finished. An attempt that is still
// "running" in durable state has no such checkpoint and may replay.
type NodeContinuation struct {
	Kind             string            `json:"kind"`
	Candidate        int               `json:"candidate"`
	CandidateAttempt int               `json:"candidateAttempt"`
	Failure          *ExecutionFailure `json:"failure,omitempty"`
	At               string            `json:"at"`
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
	ResourceWait            []ResourceRequest `json:"resourceWait,omitempty"`
	Status                  string            `json:"status"`
	IncomingNode            string            `json:"incomingNode,omitempty"`
	IncomingSessionID       string            `json:"incomingSessionId,omitempty"`
	IncomingExecutor        string            `json:"incomingExecutor,omitempty"`
	IncomingSessionProtocol string            `json:"incomingSessionProtocol,omitempty"`
	SelectedExecutor        string            `json:"selectedExecutor,omitempty"`
	SelectedCandidate       *int              `json:"selectedCandidate,omitempty"`
	Workspace               string            `json:"workspace,omitempty"`
	SessionID               string            `json:"sessionId,omitempty"`
	SessionProtocol         string            `json:"sessionProtocol,omitempty"`
	Reason                  string            `json:"reason,omitempty"`
	FallbackReason          string            `json:"fallbackReason,omitempty"`
	Attempts                int               `json:"attempts,omitempty"`
	AttemptSequence         []AttemptEvidence `json:"attemptSequence,omitempty"`
	Continuation            *NodeContinuation `json:"continuation,omitempty"`
	Failure                 *ExecutionFailure `json:"failure,omitempty"`
	DurationMs              int64             `json:"durationMs,omitempty"`
	WaitDurationMs          int64             `json:"waitDurationMs,omitempty"`
	InputBytes              int64             `json:"inputBytes,omitempty"`
	OutputBytes             int64             `json:"outputBytes,omitempty"`
	StdoutBytes             int64             `json:"stdoutBytes,omitempty"`
	StderrBytes             int64             `json:"stderrBytes,omitempty"`
	Error                   string            `json:"error,omitempty"`
	StartedAt               string            `json:"startedAt,omitempty"`
	WaitingStartedAt        string            `json:"waitingStartedAt,omitempty"`
	FinishedAt              string            `json:"finishedAt,omitempty"`
	Result                  any               `json:"result,omitempty"`
	Input                   any               `json:"input,omitempty"`
	Artifacts               map[string]string `json:"artifacts,omitempty"`
	LeaseToken              string            `json:"leaseToken,omitempty"`
}

// ExecutionFailure is the stable policy input used by execution.fallback.when.
// Code is present only when a process produced an exit status.
type ExecutionFailure struct {
	Kind     string `json:"kind"`
	Started  bool   `json:"started"`
	Code     *int   `json:"code,omitempty"`
	TimedOut bool   `json:"timedOut"`
	Message  string `json:"message"`
	Executor string `json:"executor"`
	Attempt  int    `json:"attempt"`
	Signal   string `json:"signal,omitempty"`
}

// AttemptEvidence preserves every command attempt, including failed fallback
// candidates, without turning those intermediate failures into graph nodes.
type AttemptEvidence struct {
	ResourceClaimID  string            `json:"resourceClaimId,omitempty"`
	Sequence         int               `json:"sequence"`
	Candidate        int               `json:"candidate"`
	CandidateAttempt int               `json:"candidateAttempt"`
	Executor         string            `json:"executor"`
	Status           string            `json:"status"`
	StartedAt        string            `json:"startedAt,omitempty"`
	FinishedAt       string            `json:"finishedAt,omitempty"`
	DurationMs       int64             `json:"durationMs,omitempty"`
	Workspace        string            `json:"workspace,omitempty"`
	SessionID        string            `json:"sessionId,omitempty"`
	SessionProtocol  string            `json:"sessionProtocol,omitempty"`
	Failure          *ExecutionFailure `json:"failure,omitempty"`
	InputBytes       int64             `json:"inputBytes,omitempty"`
	OutputBytes      int64             `json:"outputBytes,omitempty"`
	StdoutBytes      int64             `json:"stdoutBytes,omitempty"`
	StderrBytes      int64             `json:"stderrBytes,omitempty"`
	Artifacts        map[string]string `json:"artifacts,omitempty"`
}

type RunState struct {
	ParentRunID    string                    `json:"parentRunId,omitempty"`
	ParentNodeID   string                    `json:"parentNodeId,omitempty"`
	Correlation    map[string]string         `json:"correlation,omitempty"`
	RunID          string                    `json:"runId"`
	WorkflowName   string                    `json:"workflowName"`
	WorkflowDigest string                    `json:"workflowDigest,omitempty"`
	Status         string                    `json:"status"`
	Inputs         map[string]string         `json:"inputs"`
	IdempotencyKey string                    `json:"idempotency_key,omitempty"`
	Nodes          map[string]*NodeState     `json:"nodes"`
	ActiveNode     string                    `json:"activeNode,omitempty"`
	ActiveNodes    []string                  `json:"activeNodes,omitempty"`
	JoinInputs     map[string]map[string]any `json:"joinInputs,omitempty"`
	Outcome        string                    `json:"outcome,omitempty"`
	Visits         map[string]int            `json:"visits,omitempty"`
	Traversals     map[string]int            `json:"traversals,omitempty"`
	History        []NodeVisit               `json:"history,omitempty"`
	CreatedAt      string                    `json:"createdAt"`
	UpdatedAt      string                    `json:"updatedAt"`
	StartedAt      string                    `json:"startedAt,omitempty"`
	FinishedAt     string                    `json:"finishedAt,omitempty"`
	DurationMs     int64                     `json:"durationMs,omitempty"`
	Trigger        *TriggerInfo              `json:"trigger,omitempty"`
	Evaluation     *Evaluation               `json:"evaluation,omitempty"`
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
	Token  string         `json:"token,omitempty"`
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
