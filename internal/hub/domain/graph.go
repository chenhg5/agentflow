package domain

// Graph is the shape of a workflow: what the Hub draws on the run page.
type Graph struct {
	Entry string
	Nodes []GraphNode
	Edges []GraphEdge
}

// GraphNode is one node; Executors are its candidates in priority order.
type GraphNode struct {
	ID        string
	Executors []string
	Parallel  bool
	Join      []string
}

// GraphEdge is one transition; Target "$end" ends the run.
type GraphEdge struct {
	Source        string
	Target        string
	Condition     string
	MaxIterations int
	Parallel      bool
}

// EndNode is the edge target that ends a run.
const EndNode = "$end"

// GraphKey identifies the graph of a run. Every run of one workflow version
// shares it, so the Hub stores one copy per version.
func GraphKey(run Run) string {
	if run.Digest != "" {
		return run.Workflow + "@" + run.Digest
	}
	return "run:" + run.Key.String()
}
