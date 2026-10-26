package workspace

import (
	"sort"
	"strings"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/template"
)

type AvailabilityCheck struct {
	Workflow    string   `json:"workflow"`
	Node        string   `json:"node"`
	Candidate   int      `json:"candidate"`
	Executor    string   `json:"executor"`
	Source      string   `json:"source,omitempty"`
	Incoming    string   `json:"incoming,omitempty"`
	SourceRoute string   `json:"source_route,omitempty"`
	Protocols   []string `json:"source_protocols,omitempty"`
	Reference   string   `json:"reference"`
	Status      string   `json:"status"`
	ReasonCode  string   `json:"reason_code"`
	Message     string   `json:"message"`
}

func candidateAvailabilityChecks(root, path string, flow model.Workflow, node model.Node, index int, candidate model.ExecutorCandidate, entry driver.Executor, executors map[string]driver.Executor, inputs map[string]string, incoming *string, selected map[string]staticSourceSessionScenario) []AvailabilityCheck {
	checks := []AvailabilityCheck{}
	add := func(source, reference, status, code, message string) {
		check := AvailabilityCheck{Workflow: path, Node: node.ID, Candidate: index, Executor: candidate.Executor, Source: source, Reference: reference, Status: status, ReasonCode: code, Message: message}
		if incoming != nil {
			check.Incoming = *incoming
		}
		if reference == "session" {
			scenarios := []staticSourceSessionScenario{}
			if scenario, ok := selected[source]; ok {
				scenarios = append(scenarios, scenario)
				check.SourceRoute = scenario.route
			} else {
				scenarios = staticSourceSessionScenariosWithStack(root, flow, source, inputs, executors, map[string]bool{})
			}
			seen := map[string]bool{}
			for _, scenario := range scenarios {
				for _, outcome := range sessionOutcomes(scenario) {
					protocol := "unknown"
					if !outcome.sessionPossible {
						protocol = "no-session"
					} else if outcome.protocolKnown {
						protocol = outcome.protocol
					}
					if !seen[protocol] {
						check.Protocols = append(check.Protocols, protocol)
						seen[protocol] = true
					}
				}
			}
			sort.Strings(check.Protocols)
		}
		checks = append(checks, check)
	}
	mode := candidate.Session.Mode
	if mode == "inherit" || mode == "fork" {
		source := candidate.Session.From
		if source == "" && incoming != nil {
			source = *incoming
		}
		if failure := staticSessionFailureWithStack(root, flow, node, candidate, inputs, executors, map[string]bool{}, incoming, selected); failure != nil {
			add(source, "session", "unavailable", "session_incompatible", failure.Message)
		} else if sessionFailurePossible(root, flow, node, candidate, inputs, executors, map[string]bool{}, incoming, selected) {
			add(source, "session", "conditional", "source_protocol_conditional", "Source may have a compatible protocol, an incompatible protocol or no session; runtime selection/history is required.")
		} else {
			add(source, "session", "conditional", "session_id_unverified", "Modeled source protocol is compatible; no actual provider session ID or provider execution has been verified.")
		}
	}
	if mode == "fixed" && candidate.Session.ID == "" {
		add(node.ID, "session_first_visit", "conditional", "fixed_first_visit_new", "First visit starts fresh; it cannot assume a session from a future visit.")
		if possibleRevisit(flow, node.ID, inputs) {
			probe := probeCandidateLaunch(root, flow, node, candidate, entry, inputs, mode, "static-existing-session", incoming, executors)
			if probe.failure != nil {
				add(node.ID, "session_revisit", "conditional", "fixed_revisit_launch_failure", "If a compatible session was recorded, a revisit may fail its resumed launch: "+probe.failure.Message)
			} else {
				add(node.ID, "session_revisit", "conditional", "fixed_history_unverified", "Only a compatible session recorded by an earlier attempt can be reused; visit/history remains conditional.")
			}
		}
	}
	refs := template.References(candidate.Config)
	for _, value := range []any{entry.Command, entry.ResumeCommand, entry.ForkCommand, entry.Env} {
		refs = append(refs, template.References(value)...)
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		parts := strings.Split(ref, ".")
		if len(parts) < 3 || parts[0] != "nodes" || (parts[2] != "input" && parts[2] != "result") {
			continue
		}
		reference := strings.Join(parts[:3], ".")
		if seen[reference] {
			continue
		}
		seen[reference] = true
		availability := nodeValueAvailability(root, flow, parts[1], node.ID, inputs, incoming, parts[2] == "result", executors, map[string]bool{})
		status, code, message := "conditional", "completion_conditional", "Container availability depends on completion, routing, revisit or an external waiting fallback."
		if availability == staticValuePresent {
			status, code, message = "guaranteed", "container_ordered", "Graph ordering guarantees this container at activation; individual JSON fields still require an output/input contract."
		}
		if availability == staticValueMissing {
			status, code, message = "unavailable", "container_unavailable", "This container is absent at the modeled activation; a future or same-wave result cannot be assumed."
		}
		add(parts[1], reference, status, code, message)
	}
	return checks
}

func sortAvailabilityChecks(checks []AvailabilityCheck) {
	sort.SliceStable(checks, func(i, j int) bool {
		a, b := checks[i], checks[j]
		if a.Workflow != b.Workflow {
			return a.Workflow < b.Workflow
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		if a.Candidate != b.Candidate {
			return a.Candidate < b.Candidate
		}
		left := strings.Join([]string{a.Source, a.Incoming, a.SourceRoute, a.Reference, a.Status, a.ReasonCode, strings.Join(a.Protocols, ",")}, "\x00")
		right := strings.Join([]string{b.Source, b.Incoming, b.SourceRoute, b.Reference, b.Status, b.ReasonCode, strings.Join(b.Protocols, ",")}, "\x00")
		return left < right
	})
}

func sessionOutcomes(scenario staticSourceSessionScenario) []staticSourceSessionScenario {
	if len(scenario.alternatives) == 0 {
		return []staticSourceSessionScenario{scenario}
	}
	var outcomes []staticSourceSessionScenario
	for _, alternative := range scenario.alternatives {
		outcomes = append(outcomes, sessionOutcomes(alternative)...)
	}
	return outcomes
}

// possibleRevisit includes unknown conditions. It proves only a possible later
// visit, so it must never replace guaranteedRevisit's first-visit error proof.
func possibleRevisit(flow model.Workflow, nodeID string, inputs map[string]string) bool {
	seen := map[string]bool{nodeID: true}
	queue := []string{nodeID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		edges, _ := staticSelectedEdges(flow, current, inputs)
		for _, edge := range edges {
			if edge.To == nodeID {
				return true
			}
			if edge.To != "$end" && !seen[edge.To] {
				seen[edge.To] = true
				queue = append(queue, edge.To)
			}
		}
	}
	return false
}

// sessionFailurePossible preserves every source protocol/no-session outcome.
// A possibly compatible branch permits launch but cannot erase another branch's
// session_incompatible failure, which may select an external waiting fallback.
func sessionFailurePossible(root string, flow model.Workflow, node model.Node, candidate model.ExecutorCandidate, inputs map[string]string, executors map[string]driver.Executor, visiting map[string]bool, incoming *string, selected map[string]staticSourceSessionScenario) bool {
	if candidate.Session.Mode != "inherit" && candidate.Session.Mode != "fork" {
		return false
	}
	sources := []string{candidate.Session.From}
	if candidate.Session.From == "" {
		if incoming != nil {
			sources = []string{*incoming}
		} else {
			sources = incomingNodes(flow, node.ID)
		}
	}
	for _, source := range sources {
		scenarios := []staticSourceSessionScenario{}
		if scenario, ok := selected[source]; ok {
			scenarios = append(scenarios, scenario)
		} else {
			scenarios = staticSourceSessionScenariosWithStack(root, flow, source, inputs, executors, visiting)
		}
		for _, scenario := range scenarios {
			for _, outcome := range sessionOutcomes(scenario) {
				if !outcome.sessionPossible || !outcome.protocolKnown || !driver.SessionProtocolCompatible(candidate.Executor, executors[candidate.Executor], outcome.protocol) {
					return true
				}
			}
		}
	}
	return false
}
