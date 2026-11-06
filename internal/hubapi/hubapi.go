// Package hubapi is the wire contract between a workflow runner and the Hub.
//
// A runner reports the latest snapshot of a run every time its state changes.
// Snapshots are idempotent: the Hub keeps the one with the highest Seq per run,
// so retries, duplicates and out-of-order deliveries are harmless.
//
// The contract does not depend on how a run is orchestrated. A JSON workflow
// run by the local engine and a future code-orchestrated workflow report the
// same RunReport; Orchestration only tells the Hub which one produced it.
package hubapi

// ProtocolVersion is bumped only for incompatible changes.
const ProtocolVersion = 1

// IngestPath is the HTTP endpoint that accepts a Batch.
const IngestPath = "/api/v1/reports"

// MaxBatch is the largest number of reports accepted in one request.
const MaxBatch = 100

// Orchestration styles a report can come from.
const (
	OrchestrationJSON = "json"
	OrchestrationCode = "code"
)

// Batch is the body of a POST to IngestPath.
type Batch struct {
	Reports []RunReport `json:"reports"`
}

// IngestResult is the response to a Batch.
type IngestResult struct {
	Accepted int      `json:"accepted"`
	Stale    int      `json:"stale"`
	Rejected []string `json:"rejected,omitempty"`
}

// Source identifies where a run executed.
type Source struct {
	Machine  string `json:"machine"`
	Hostname string `json:"hostname,omitempty"`
	Project  string `json:"project,omitempty"`
	Path     string `json:"path,omitempty"`
	Version  string `json:"version,omitempty"`
}

// Trigger describes what started a run.
type Trigger struct {
	ID          string `json:"id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	DeliveryID  string `json:"deliveryId,omitempty"`
	ScheduledAt string `json:"scheduledAt,omitempty"`
}

// RunReport is the latest known state of one run.
type RunReport struct {
	Protocol       int               `json:"protocol"`
	Source         Source            `json:"source"`
	Orchestration  string            `json:"orchestration"`
	RunID          string            `json:"runId"`
	Workflow       string            `json:"workflow"`
	WorkflowDigest string            `json:"workflowDigest,omitempty"`
	Status         string            `json:"status"`
	Seq            int64             `json:"seq"`
	CreatedAt      string            `json:"createdAt,omitempty"`
	StartedAt      string            `json:"startedAt,omitempty"`
	FinishedAt     string            `json:"finishedAt,omitempty"`
	UpdatedAt      string            `json:"updatedAt,omitempty"`
	DurationMs     int64             `json:"durationMs,omitempty"`
	WaitingMs      int64             `json:"waitingMs,omitempty"`
	Trigger        *Trigger          `json:"trigger,omitempty"`
	Inputs         map[string]string `json:"inputs,omitempty"`
	Error          string            `json:"error,omitempty"`
	Steps          []StepReport      `json:"steps"`
	// Graph is the workflow's shape, for the Hub's canvas. It is the same for
	// every run of one workflow version, so the Hub keeps one per digest.
	Graph *Graph `json:"graph,omitempty"`
}

// StepReport is one node of a JSON workflow, or one step of a code workflow.
//
// Summary and Reason come from the reserved result fields `summary` and
// `reason`, so a workflow can say what a step concluded without extra code.
type StepReport struct {
	ID          string `json:"id"`
	Type        string `json:"type,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Status      string `json:"status"`
	SkipReason  string `json:"skipReason,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	WaitingMs   int64  `json:"waitingMs,omitempty"`
	Error       string `json:"error,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Reason      string `json:"reason,omitempty"`
	OutputBytes int64  `json:"outputBytes,omitempty"`
	LogBytes    int64  `json:"logBytes,omitempty"`
	// The ends of the step's latest attempt, masked and clipped: the last
	// bytes of its logs and a preview of its result JSON. A report carries
	// them within a budget, failed and running steps first; the Hub keeps a
	// tail it already has when a later report of the same visit leaves it out.
	StdoutTail string     `json:"stdoutTail,omitempty"`
	StderrTail string     `json:"stderrTail,omitempty"`
	Result     string     `json:"result,omitempty"`
	Artifacts  []Artifact `json:"artifacts,omitempty"`
}

// Artifact names one file a step produced; only its size leaves the machine.
type Artifact struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Graph describes a workflow as nodes and edges. A code-orchestrated runner
// may omit it, or send the steps it discovered as nodes with no edges.
type Graph struct {
	Entry string      `json:"entry,omitempty"`
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// GraphNode is one node; Executors are its candidates in priority order.
type GraphNode struct {
	ID        string   `json:"id"`
	Executors []string `json:"executors,omitempty"`
	Parallel  bool     `json:"parallel,omitempty"` // dispatches to every matching edge
	Join      []string `json:"join,omitempty"`     // waits for these predecessors
}

// GraphEdge is one transition. Target "$end" ends the run.
type GraphEdge struct {
	Source        string `json:"source"`
	Target        string `json:"target"`
	Condition     string `json:"condition,omitempty"`
	MaxIterations int    `json:"maxIterations,omitempty"`
	Parallel      bool   `json:"parallel,omitempty"`
}
