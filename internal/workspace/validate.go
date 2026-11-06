package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/agentflow/internal/condition"
	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/policy"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/template"
	"github.com/chenhg5/agentflow/internal/trigger"
)

type Issue struct {
	Severity   string `json:"severity"`
	Path       string `json:"path"`
	Message    string `json:"message"`
	Node       string `json:"node,omitempty"`
	Candidate  *int   `json:"candidate,omitempty"`
	Source     string `json:"source,omitempty"`
	ReasonCode string `json:"reason_code"`
}

type Validation struct {
	Root              string              `json:"root"`
	LayoutVersion     string              `json:"layout_version"`
	Valid             bool                `json:"valid"`
	ExecutionVerified bool                `json:"execution_verified"`
	Issues            []Issue             `json:"issues"`
	Bindings          []map[string]any    `json:"bindings"`
	RunStore          string              `json:"run_store"`
	AgentResources    []AgentResources    `json:"agent_resources"`
	Analysis          []AvailabilityCheck `json:"analysis"`
}

func Validate(root, workflowPath string) Validation {
	return ValidateWithInputs(root, workflowPath, nil)
}

// ValidateWithInputs resolves the static subset of candidate bindings using
// representative run inputs. It does not execute nodes or simulate their output.
func ValidateWithInputs(root, workflowPath string, inputs map[string]string) Validation {
	result := Validation{Root: root, LayoutVersion: LayoutVersion, Valid: true, Issues: []Issue{}, Bindings: []map[string]any{}, RunStore: filepath.Join(root, "runs"), AgentResources: []AgentResources{}, Analysis: []AvailabilityCheck{}}
	issue := func(severity, path, message string) {
		code := "static_warning"
		if severity == "error" {
			code = "validation_error"
		}
		result.Issues = append(result.Issues, Issue{Severity: severity, Path: path, Message: message, ReasonCode: code})
		if severity == "error" {
			result.Valid = false
		}
	}
	executors, err := driver.LoadExecutors(root)
	if err != nil {
		issue("error", "executors.json", err.Error())
		return result
	}
	paths := []string{}
	if workflowPath != "" {
		paths = append(paths, workflowPath)
	} else if _, err := os.Stat(filepath.Join(root, "workflow.json")); !os.IsNotExist(err) {
		paths = append(paths, "workflow.json")
	}
	if _, err := os.Stat(filepath.Join(root, "triggers.json")); err == nil {
		config, err := trigger.LoadConfig(root)
		if err != nil {
			issue("error", "triggers.json", err.Error())
		} else {
			for _, definition := range config.Triggers {
				if definition.Enabled {
					paths = append(paths, definition.Target.Workflow)
				}
			}
		}
	}
	if len(paths) == 0 {
		issue("error", "workflow.json", "no default graph or enabled trigger target; select a graph with --workflow")
	}
	// checkCandidate returns whether a candidate is statically runnable and,
	// when it is definitely unavailable, the structured failure it would
	// produce. Later candidates are reachable only when the node's fallback
	// policy selects that known failure.
	checkCandidate := func(flow model.Workflow, path, nodeID string, index int, candidate model.ExecutorCandidate, entry driver.Executor, incomingSource string, incomingKnown bool, selectedSessions map[string]staticSourceSessionScenario) (bool, []*model.ExecutionFailure) {
		label := fmt.Sprintf("%s:%s:candidate-%d", path, nodeID, index+1)
		if incomingKnown {
			label += ":incoming-" + incomingSource
		}
		node := workflowNode(flow, nodeID)
		var selectedIncoming *string
		if incomingKnown {
			selectedIncoming = &incomingSource
		}
		checks := candidateAvailabilityChecks(root, path, flow, node, index, candidate, entry, executors, inputs, selectedIncoming, selectedSessions)
		result.Analysis = append(result.Analysis, checks...)
		report := func(code, message string) {
			issue("warning", label, message)
			entry := &result.Issues[len(result.Issues)-1]
			entry.Node, entry.Candidate, entry.ReasonCode = nodeID, &index, code
			entry.Source = candidate.Session.From
			if entry.Source == "" && incomingKnown {
				entry.Source = incomingSource
			}
		}
		for _, check := range checks {
			if check.ReasonCode == "source_protocol_conditional" || check.ReasonCode == "fixed_revisit_launch_failure" || check.ReasonCode == "completion_conditional" {
				report(check.ReasonCode, check.Message)
				result.Issues[len(result.Issues)-1].Source = check.Source
			}
		}
		possibleFailures := []*model.ExecutionFailure{}
		unavailable := func(kind, message string) (bool, []*model.ExecutionFailure) {
			report(kind, message)
			possibleFailures = append(possibleFailures, &model.ExecutionFailure{Kind: kind, Started: false, TimedOut: false, Message: message, Executor: candidate.Executor, Attempt: 0})
			return false, possibleFailures
		}
		// Runtime resolves the candidate's session before Prepare and the
		// workspace/executable availability checks. Mirror the deterministic
		// cases here so an impossible inherit/fork source is reported as
		// session_incompatible instead of a startup failure that a
		// start_failed-only default fallback could incorrectly select.
		if failure := staticSessionFailureForRoute(root, flow, node, candidate, inputs, executors, incomingSource, incomingKnown, selectedSessions); failure != nil {
			return unavailable(failure.Kind, failure.Message)
		}
		mode := candidate.Session.Mode
		if mode == "" {
			mode = "new"
		}
		sessionID := ""
		if mode == "inherit" || mode == "fork" {
			sessionID = "static-existing-session"
		}
		if mode == "fixed" && candidate.Session.ID != "" {
			resolvedID, known := staticFixedSessionID(flow, node, candidate.Session.ID, inputs)
			if !known {
				issue("warning", label, "fixed session id contains runtime references; session availability is inconclusive")
				possibleFailures = append(possibleFailures, &model.ExecutionFailure{
					Kind: "session_incompatible", Started: false, TimedOut: false,
					Message:  fmt.Sprintf("node %s candidate %s fixed session id may resolve to an empty value", node.ID, candidate.Executor),
					Executor: candidate.Executor, Attempt: 0,
				})
				// Probe the non-empty alternative as an existing session. It is the
				// only branch that could launch and make this candidate usable.
				sessionID = "static-existing-session"
			} else if resolvedID == "" {
				return unavailable("session_incompatible", fmt.Sprintf("node %s candidate %s fixed session id resolved to an empty value", node.ID, candidate.Executor))
			} else {
				sessionID = resolvedID
			}
		}
		if entry.Kind == "external" {
			// Runtime resolves the session before it consults the external adapter.
			// In particular, a known-empty fixed id is session_incompatible rather
			// than the adapter's later configuration_failed result.
			if len(entry.Command) > 0 {
				return unavailable("configuration_failed", fmt.Sprintf("external executor %s cannot define command", candidate.Executor))
			}
			if mode != "new" {
				return unavailable("configuration_failed", fmt.Sprintf("external executor %s does not support session mode %s", candidate.Executor, mode))
			}
			result.Bindings = append(result.Bindings, map[string]any{"workflow": path, "node": nodeID, "candidate": index, "executor": candidate.Executor, "environment": driver.EnvironmentDescription(entry.Environment)})
			return true, nil
		}
		probe := probeCandidateLaunch(root, flow, node, candidate, entry, inputs, mode, sessionID, selectedIncoming, executors)
		for _, warning := range fixedSessionWarnings(candidate.Executor, entry, candidate) {
			issue("warning", label, warning)
		}
		if probe.failure != nil {
			report(probe.failure.Kind, probe.failure.Message)
			possibleFailures = append(possibleFailures, probe.preLaunchFailures(candidate.Executor, 0)...)
			return false, possibleFailures
		}
		// A fixed policy without an id is fresh only on its first successful
		// visit. On a guaranteed cycle the next visit reuses the recorded id, so
		// validate the actual resumed command as part of reachability as well.
		if mode == "fixed" && candidate.Session.ID == "" && guaranteedRevisit(flow, node.ID, inputs) {
			resumed := probeCandidateLaunch(root, flow, node, candidate, entry, inputs, mode, "static-existing-session", selectedIncoming, executors)
			if resumed.failure != nil {
				issue("warning", label, "guaranteed revisit: "+resumed.failure.Message)
				return false, resumed.preLaunchFailures(candidate.Executor, 0)
			}
			if resumed.inconclusive {
				issue("warning", label, resumed.inconclusiveMessage)
				return true, nil
			}
		}
		if probe.inconclusive {
			report("launch_inconclusive", probe.inconclusiveMessage)
			return true, nil
		}
		result.Bindings = append(result.Bindings, map[string]any{"workflow": path, "node": nodeID, "candidate": index, "executor": candidate.Executor, "workspace": probe.directory, "resolved_workspace": probe.resolvedDirectory, "environment": driver.EnvironmentDescription(entry.Environment)})
		return true, nil
	}
	seen := map[string]bool{}
	for _, path := range paths {
		absolute := path
		if !filepath.IsAbs(path) {
			absolute = filepath.Join(root, path)
		}
		if seen[absolute] {
			continue
		}
		seen[absolute] = true
		data, err := os.ReadFile(absolute)
		if err != nil {
			issue("error", path, err.Error())
			continue
		}
		for _, problem := range schema.ValidateWorkflowJSON(data) {
			issue("error", path, problem)
		}
		var flow model.Workflow
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&flow); err != nil {
			issue("error", path, err.Error())
			continue
		}
		if err := store.ValidateResources(root, flow); err != nil {
			issue("error", path, err.Error())
		}
		for _, problem := range schema.Validate(flow) {
			issue("error", path, problem)
		}
		for _, node := range flow.Nodes {
			hasRouteSensitiveActivation := nodeHasRouteSensitiveCommandReference(node)
			for index, candidate := range node.Execution.Candidates {
				entry, ok := executors[candidate.Executor]
				if !ok {
					issue("error", fmt.Sprintf("%s:%s:candidate-%d", path, node.ID, index+1), fmt.Sprintf("unregistered executor %q", candidate.Executor))
				}
				if ok && (entry.Kind == "codex" || entry.Kind == "claude-code") {
					resources := inspectAgent(root, path, flow, node, index, candidate, entry, inputs)
					result.AgentResources = append(result.AgentResources, resources)
					for _, message := range resources.Issues {
						issue("warning", fmt.Sprintf("%s:%s:candidate-%d", path, node.ID, index+1), message)
					}
				}
				mode := candidate.Session.Mode
				if mode == "inherit" || mode == "fork" {
					hasRouteSensitiveActivation = true
				}
			}
			incomingRoutes := []string{""}
			incomingKnown := false
			if hasRouteSensitiveActivation && node.ID != flow.Entry {
				if sources, resolved := staticIncomingRoutes(flow, node.ID, inputs); len(sources) > 0 {
					incomingRoutes = sources
					incomingKnown = true
					if !resolved {
						issue("warning", path+":"+node.ID, "incoming route is unresolved; validating route-sensitive candidate state against every statically reachable predecessor")
					}
				}
			}
			maxAttempts := node.Retry.MaxAttempts
			if maxAttempts < 1 {
				maxAttempts = 1
			}
			if guaranteedRevisit(flow, node.ID, inputs) && policy.UsesRuntimeAttempt(node.Execution.Fallback) {
				issue("warning", path+":"+node.ID, "fallback policy depends on the global failure attempt across a guaranteed revisit; the cross-visit attempt sequence is inconclusive")
			}
			sessionScenarios := []map[string]staticSourceSessionScenario{nil}
			if source, single := singleExplicitSessionSource(node); single && workflowHasNode(flow, source) {
				sourceScenarios := staticSourceSessionScenariosWithStack(root, flow, source, inputs, executors, map[string]bool{})
				if len(sourceScenarios) > 0 {
					sessionScenarios = make([]map[string]staticSourceSessionScenario, 0, len(sourceScenarios))
					for _, scenario := range sourceScenarios {
						sessionScenarios = append(sessionScenarios, map[string]staticSourceSessionScenario{source: scenario})
					}
					if len(sourceScenarios) > 1 {
						issue("warning", path+":"+node.ID, "explicit session source route is unresolved; validating candidate fallback against every statically reachable source route")
					}
				}
			}
			for _, incomingSource := range incomingRoutes {
				for _, selectedSessions := range sessionScenarios {
					usable := 0
					reachable := true
					// Runtime fallback sees the 1-based global attempt sequence, and a
					// statically unavailable candidate exhausts every retry before the
					// policy is evaluated. This cursor describes the first visit only;
					// attempt-sensitive guaranteed revisits are reported as inconclusive
					// above instead of pretending each visit restarts at one.
					attemptCursor := 1
					for index, candidate := range node.Execution.Candidates {
						label := fmt.Sprintf("%s:%s:candidate-%d", path, node.ID, index+1)
						if incomingKnown {
							label += ":incoming-" + incomingSource
						}
						entry, exists := executors[candidate.Executor]
						if !exists {
							reachable = false
							continue
						}
						if !reachable {
							continue
						}
						available, failures := checkCandidate(flow, path, node.ID, index, candidate, entry, incomingSource, incomingKnown, selectedSessions)
						if available {
							usable++
							// A statically available candidate still consumes its full
							// retry budget before a later candidate is considered, so
							// later failures use the same global attempt position the
							// runtime would report if this candidate failed.
							attemptCursor += maxAttempts
							continue
						}
						// Model the failure the runtime policy would observe: the last
						// attempt of this candidate before fallback is considered.
						for _, failure := range failures {
							failure.Attempt = attemptCursor + maxAttempts - 1
						}
						failure := failures[0]
						consumed := maxAttempts
						if index == len(node.Execution.Candidates)-1 {
							reachable = false
							continue
						}
						matched, constant := policy.ConstantFallbackResult(node.Execution.Fallback)
						if !constant && policy.UsesRuntimeMessage(node.Execution.Fallback) {
							// The exact driver error text is not reproducible statically,
							// so a synthetic message can prove neither reachability nor
							// unreachability. Keep later candidates conservatively
							// reachable and report the inconclusive result.
							issue("warning", label, "fallback policy depends on the runtime failure message; static reachability is inconclusive")
							attemptCursor += consumed
							continue
						}
						if !constant {
							var evaluationError error
							for _, possible := range failures {
								selected, err := policy.AllowsFallback(node.Execution.Fallback, possible)
								matched = matched || selected
								if err != nil {
									evaluationError = err
								}
							}
							if evaluationError != nil {
								severity := "warning"
								if !matched {
									severity = "error"
								}
								issue(severity, label, "fallback policy cannot be evaluated for a possible unavailable-candidate failure: "+evaluationError.Error())
							}
						}
						if !matched {
							issue("warning", label, fmt.Sprintf("later candidates are unreachable: fallback policy does not select a %s failure", failure.Kind))
							reachable = false
						}
						attemptCursor += consumed
					}
					if usable == 0 {
						label := path + ":" + node.ID
						message := "no usable execution candidate"
						if incomingKnown {
							label += ":incoming-" + incomingSource
							message += " for incoming route from " + incomingSource
						}
						issue("error", label, message)
						problem := &result.Issues[len(result.Issues)-1]
						problem.Node, problem.ReasonCode = node.ID, "no_usable_execution_candidate"
						if incomingKnown {
							problem.Source = incomingSource
						}
					}
				}
			}
		}
	}
	sortAvailabilityChecks(result.Analysis)
	return result
}

type staticLaunchProbe struct {
	directory                    string
	resolvedDirectory            string
	inconclusive                 bool
	configurationFailurePossible bool
	inconclusiveMessage          string
	failure                      *model.ExecutionFailure
}

// preLaunchFailures keeps uncertain preparation failures separate from a
// deterministic condition that rules out successful process launch.
func (probe staticLaunchProbe) preLaunchFailures(executor string, attempt int) []*model.ExecutionFailure {
	var failures []*model.ExecutionFailure
	if probe.failure != nil {
		failure := *probe.failure
		failure.Attempt = attempt
		failures = append(failures, &failure)
	}
	if probe.configurationFailurePossible && (probe.failure == nil || probe.failure.Kind != "configuration_failed") {
		failures = append(failures, &model.ExecutionFailure{Kind: "configuration_failed", Started: false, TimedOut: false, Executor: executor, Attempt: attempt})
	}
	return failures
}

// probeCandidateLaunch checks independent known launch requirements even when
// another requirement depends on future input. A failure rules out success;
// preLaunchFailures retains each failure class that may win at runtime.
func probeCandidateLaunch(root string, flow model.Workflow, node model.Node, candidate model.ExecutorCandidate, entry driver.Executor, inputs map[string]string, mode, sessionID string, incomingSource *string, executors map[string]driver.Executor, availabilityVisiting ...map[string]bool) staticLaunchProbe {
	directory, unresolved := candidateDirectory(flow, node, candidate, entry, inputs)
	probe := staticLaunchProbe{directory: directory}
	fail := func(kind, message string) staticLaunchProbe {
		probe.failure = &model.ExecutionFailure{Kind: kind, Started: false, TimedOut: false, Message: message, Executor: candidate.Executor}
		return probe
	}
	preparedDirectory := directory
	if !filepath.IsAbs(preparedDirectory) {
		preparedDirectory = filepath.Join(root, filepath.FromSlash(preparedDirectory))
	}
	entry.Workspace = preparedDirectory
	input, config, configContext := staticCandidateValues(flow, node, candidate, inputs)
	usesConfiguredCommand := sessionID == "" && entry.Kind != "codex" && entry.Kind != "claude-code" && len(entry.Command) == 0
	configuredCommand := staticConfiguredCommandAnalysis{}
	if usesConfiguredCommand {
		var visiting map[string]bool
		if len(availabilityVisiting) > 0 {
			visiting = availabilityVisiting[0]
		}
		configuredCommand = analyzeStaticConfiguredCommand(root, candidate.Config["command"], configContext, flow, node, inputs, incomingSource, executors, visiting)
	}
	launchContext := map[string]any{"input": input, "config": config}
	rawCommand := entry.Command
	if sessionID != "" {
		if mode == "fork" {
			rawCommand = entry.ForkCommand
		} else {
			rawCommand = entry.ResumeCommand
		}
	}
	unknownExecutable := configuredCommand.executableUnknown || (len(rawCommand) > 0 && len(staticUnresolvedReferences(rawCommand[0], launchContext, node.ID == flow.Entry, flow.Inputs)) > 0)
	unknownInvocation := configuredCommand.unresolved || len(staticUnresolvedReferences(rawCommand, launchContext, node.ID == flow.Entry, flow.Inputs)) > 0 || len(staticUnresolvedReferences(entry.Env, launchContext, node.ID == flow.Entry, flow.Inputs)) > 0
	probe.configurationFailurePossible = configuredCommand.configurationFailurePossible
	if unknownInvocation {
		probe.inconclusive = true
		probe.inconclusiveMessage = "candidate command or environment contains runtime templates; launch availability is inconclusive"
		if configuredCommand.unresolved {
			probe.inconclusiveMessage = "candidate config contains runtime templates; launch availability is inconclusive"
		}
	}
	if configuredCommand.shapeInvalid {
		probe.inconclusive = false
		return fail("configuration_failed", "candidate config.command contains a value whose known type cannot form a string command array")
	}
	prepared, err := driver.Prepare(candidate.Executor, entry, driver.Invocation{
		Input: input, Config: config, SessionMode: mode, SessionID: sessionID,
		ResultFile: "static-result.json", RunID: "static-validation", NodeID: node.ID,
	})
	if err != nil && !configuredCommand.shapeUnknown {
		probe.inconclusive = false
		return fail("configuration_failed", err.Error())
	}
	workspaceKnown := len(unresolved) == 0
	if workspaceKnown {
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(root, filepath.FromSlash(directory))
		}
		probe.directory = directory
		info, err := os.Stat(directory)
		if err != nil {
			return fail("start_failed", "candidate workspace unavailable: "+err.Error())
		}
		if !info.IsDir() {
			return fail("start_failed", "candidate workspace is not a directory")
		}
		resolved, err := filepath.EvalSymlinks(directory)
		if err != nil {
			return fail("start_failed", err.Error())
		}
		probe.resolvedDirectory = resolved
	} else {
		probe.inconclusive = true
		probe.inconclusiveMessage = "workspace contains runtime templates; validate it with representative run inputs"
	}
	if configuredCommand.shapeUnknown || unknownExecutable {
		return probe
	}
	// An absolute executable or PATH lookup can be checked independently of
	// an unknown working directory. Relative executable paths require it.
	if !workspaceKnown {
		if len(prepared.Command) == 0 || (!filepath.IsAbs(prepared.Command[0]) && strings.ContainsAny(prepared.Command[0], `/\`)) {
			return probe
		}
		entry.Workspace = root
	}
	entry.Command, entry.ResumeCommand, entry.ForkCommand = prepared.Command, nil, nil
	if status := driver.CheckExecutor(candidate.Executor, entry); !status.Available {
		return fail("start_failed", "candidate executable unavailable: "+status.Error)
	}
	return probe
}

type staticConfiguredCommandAnalysis struct {
	unresolved                   bool
	shapeUnknown                 bool
	shapeInvalid                 bool
	executableUnknown            bool
	configurationFailurePossible bool
}

// analyzeStaticConfiguredCommand preserves the facts that remain known when
// config.command contains future input. An exact top-level reference may
// change the command container's type, while an interpolated scalar remains a
// deterministic shape error. Within a known array, exact references may render
// non-strings, but unresolved later arguments do not hide a known executable.
func analyzeStaticConfiguredCommand(root string, value any, context map[string]any, flow model.Workflow, node model.Node, inputs map[string]string, incomingSource *string, executors map[string]driver.Executor, availabilityVisiting map[string]bool) staticConfiguredCommandAnalysis {
	entry := node.ID == flow.Entry
	analysis := staticConfiguredCommandAnalysis{unresolved: len(staticUnresolvedReferences(value, context, entry, flow.Inputs)) > 0}
	if !analysis.unresolved {
		return analysis
	}
	switch command := value.(type) {
	case string:
		types := staticTemplateTypes(root, command, flow, node, inputs, incomingSource, executors, availabilityVisiting)
		analysis.shapeInvalid = types&staticArrayType == 0
		analysis.shapeUnknown = !analysis.shapeInvalid
		analysis.executableUnknown = !analysis.shapeInvalid
		analysis.configurationFailurePossible = !analysis.shapeInvalid
	case []any:
		for index, value := range command {
			part, ok := value.(string)
			if !ok || len(staticUnresolvedReferences(part, context, entry, flow.Inputs)) == 0 {
				continue
			}
			types := staticTemplateTypes(root, part, flow, node, inputs, incomingSource, executors, availabilityVisiting)
			if types&staticStringType == 0 {
				analysis.shapeInvalid = true
			}
			if index == 0 {
				analysis.executableUnknown = true
			}
			analysis.configurationFailurePossible = analysis.configurationFailurePossible || types & ^staticStringType != 0
		}
	}
	return analysis
}

type staticJSONTypes uint8

const (
	staticStringType staticJSONTypes = 1 << iota
	staticArrayType
	staticOtherType
	staticAnyType = staticStringType | staticArrayType | staticOtherType
)

// Template interpolation always produces text; a whole reference retains JSON
// types. These are type facts only, never fabricated representative values.
func staticTemplateTypes(root, value string, flow model.Workflow, node model.Node, inputs map[string]string, incomingSource *string, executors map[string]driver.Executor, availabilityVisiting map[string]bool) staticJSONTypes {
	if _, text := template.RenderValue(value, map[string]any{}).(string); text {
		return staticStringType
	}
	refs := template.References(value)
	if len(refs) != 1 {
		return staticAnyType
	}
	parts := strings.Split(refs[0], ".")
	switch parts[0] {
	case "input":
		if len(parts) == 1 {
			return staticOtherType
		}
		if node.ID == flow.Entry {
			if len(parts) == 2 {
				return staticStringType | staticOtherType
			}
			return staticOtherType
		}
		return staticSchemaPathTypes(node.InputSchema, parts[1:])
	case "inputs":
		if len(parts) == 1 {
			return staticOtherType
		}
		if len(parts) == 2 {
			if definition, declared := flow.Inputs[parts[1]]; declared && definition.Required {
				return staticStringType
			}
			return staticStringType | staticOtherType
		}
		return staticOtherType
	case "config":
		if len(parts) == 1 {
			return staticOtherType
		}
	case "nodes":
		if len(parts) == 1 {
			return staticOtherType
		}
		if workflowHasNode(flow, parts[1]) {
			if len(parts) == 2 {
				return staticOtherType
			}
			source := workflowNode(flow, parts[1])
			switch parts[2] {
			case "status", "sessionId":
				if len(parts) == 3 {
					return staticStringType
				}
				return staticOtherType
			case "input":
				if len(parts) == 3 {
					return staticOtherType
				}
				types := staticSchemaPathTypes(source.InputSchema, parts[3:])
				return staticNodeValueTypes(types, nodeValueAvailability(root, flow, source.ID, node.ID, inputs, incomingSource, false, executors, availabilityVisiting))
			case "result":
				if len(parts) == 3 {
					return staticOtherType
				}
				types := staticSchemaPathTypes(source.OutputSchema, parts[3:])
				return staticNodeValueTypes(types, nodeValueAvailability(root, flow, source.ID, node.ID, inputs, incomingSource, true, executors, availabilityVisiting))
			case "artifacts":
				if len(parts) == 3 {
					return staticOtherType
				}
				if len(parts) == 4 {
					return staticStringType | staticOtherType
				}
				return staticOtherType
			}
		}
	}
	return staticAnyType
}

type staticValueAvailability uint8

const (
	staticValueUnknown staticValueAvailability = iota
	staticValuePresent
	staticValueMissing
)

func staticNodeValueTypes(types staticJSONTypes, availability staticValueAvailability) staticJSONTypes {
	switch availability {
	case staticValueMissing:
		return staticOtherType
	case staticValuePresent:
		return types
	default:
		return types | staticOtherType
	}
}

// Inputs exist as soon as a node is activated; its result does not exist until
// that activation completes. Parallel fan-out initializes every selected child
// before the next preparation wave, and reactivation replaces the prior state,
// so input and result availability require separate proofs.
func nodeValueAvailability(root string, flow model.Workflow, source, target string, inputs map[string]string, incomingSource *string, result bool, executors map[string]driver.Executor, availabilityVisiting map[string]bool) staticValueAvailability {
	if result {
		return nodeResultAvailability(root, flow, source, target, inputs, incomingSource, executors, availabilityVisiting)
	}
	return nodeInputAvailability(flow, source, target, inputs, incomingSource)
}

func nodeInputAvailability(flow model.Workflow, source, target string, inputs map[string]string, incomingSource *string) staticValueAvailability {
	if source == target {
		return staticValuePresent
	}
	if source == flow.Entry {
		return staticValuePresent
	}
	sameWave := staticValueMissing
	if incomingSource != nil {
		sameWave = nodeActivatedInTargetWave(flow, source, target, *incomingSource, inputs)
		if sameWave == staticValuePresent {
			return staticValuePresent
		}
	}
	if nodeCompletedBefore(flow, source, target) ||
		(incomingSource != nil && nodeCompletedBeforeOnSelectedRoute(flow, source, target, *incomingSource, inputs)) {
		return staticValuePresent
	}
	if sameWave == staticValueUnknown {
		return staticValueUnknown
	}
	if staticSelectedPath(flow, flow.Entry, source, target, inputs) {
		return staticValueUnknown
	}
	// A cycle may have initialized the source during an earlier visit.
	if staticSelectedPath(flow, flow.Entry, source, "", inputs) && staticSelectedPath(flow, source, target, "", inputs) {
		return staticValueUnknown
	}
	return staticValueMissing
}

func nodeResultAvailability(root string, flow model.Workflow, source, target string, inputs map[string]string, incomingSource *string, executors map[string]driver.Executor, availabilityVisiting map[string]bool) staticValueAvailability {
	if source == target {
		return staticValueMissing
	}
	sameWave := staticValueMissing
	if incomingSource != nil {
		sameWave = nodeActivatedInTargetWave(flow, source, target, *incomingSource, inputs)
	}
	// A selected same-wave activation has already replaced the current
	// NodeState by the time target is prepared, clearing any prior result.
	if sameWave == staticValuePresent {
		return staticValueMissing
	}
	strictCompletion := source == flow.Entry || nodeGraphCompletedBefore(flow, source, target) ||
		(incomingSource != nil && nodeCompletedBeforeOnSelectedRoute(flow, source, target, *incomingSource, inputs))
	completed := strictCompletion || nodeCompletedBefore(flow, source, target)
	if completed {
		if sameWave == staticValueUnknown || (!strictCompletion && nodeDefersCompletion(root, flow, source, inputs, executors, availabilityVisiting)) {
			return staticValueUnknown
		}
		return staticValuePresent
	}
	if staticSelectedPath(flow, flow.Entry, source, target, inputs) {
		return staticValueUnknown
	}
	// A cycle may have completed the source during an earlier visit.
	if staticSelectedPath(flow, flow.Entry, source, "", inputs) && staticSelectedPath(flow, source, target, "", inputs) {
		return staticValueUnknown
	}
	return staticValueMissing
}

// nodeActivatedInTargetWave reports whether advancing the current active wave
// initializes source alongside target. Runtime advances every succeeded parent
// before the next preparation pass, so the activating parent may be a sibling
// of target's immediate incoming parent rather than that parent itself.
func nodeActivatedInTargetWave(flow model.Workflow, source, target, targetParent string, inputs map[string]string) staticValueAvailability {
	if source == "" || target == "" || targetParent == "" {
		return staticValueMissing
	}
	unknown := false
	for _, parent := range selectedActivationParents(flow, source, inputs) {
		if parent.node == targetParent {
			// One serial dispatch cannot select both the source and target edges.
			if workflowNode(flow, parent.node).Dispatch != "all" {
				continue
			}
			if parent.availability == staticValuePresent {
				return staticValuePresent
			}
			unknown = true
			continue
		}
		sameWave := nodesCurrentVisitSameWave(flow, parent.node, targetParent, inputs, map[nodePair]bool{})
		switch combineStaticAvailability(parent.availability, sameWave) {
		case staticValuePresent:
			return staticValuePresent
		case staticValueUnknown:
			unknown = true
		}
	}
	if unknown {
		return staticValueUnknown
	}
	return staticValueMissing
}

type staticActivationParent struct {
	node         string
	availability staticValueAvailability
}

type nodePair struct {
	left  string
	right string
}

// selectedActivationParents returns parents whose selected edge may activate
// child under the representative inputs. Per-edge selection is retained so an
// unrelated unknown dispatch-all condition does not weaken a known edge.
func selectedActivationParents(flow model.Workflow, child string, inputs map[string]string) []staticActivationParent {
	byNode := map[string]staticValueAvailability{}
	order := []string{}
	for _, edge := range flow.Edges {
		if edge.To != child || (edge.From != flow.Entry && !staticSelectedPath(flow, flow.Entry, edge.From, "", inputs)) {
			continue
		}
		availability := selectedEdgeAvailability(flow, edge, inputs)
		if availability == staticValueMissing {
			continue
		}
		if _, seen := byNode[edge.From]; !seen {
			order = append(order, edge.From)
		}
		if byNode[edge.From] != staticValuePresent {
			byNode[edge.From] = availability
		}
	}
	parents := make([]staticActivationParent, 0, len(order))
	for _, id := range order {
		parents = append(parents, staticActivationParent{node: id, availability: byNode[id]})
	}
	return parents
}

// selectedEdgeAvailability classifies one edge using runtime dispatch order.
// A serial edge after an unresolved condition is only possibly selected; a
// known earlier match makes every later edge unreachable.
func selectedEdgeAvailability(flow model.Workflow, selected model.Transition, inputs map[string]string) staticValueAvailability {
	node := workflowNode(flow, selected.From)
	priorUnknown := false
	for _, edge := range flow.Edges {
		if edge.From != selected.From {
			continue
		}
		matched, known := staticConditionMatch(edge.When, flow, edge.From, inputs)
		if edge == selected {
			if known && !matched {
				return staticValueMissing
			}
			if !known || (node.Dispatch != "all" && priorUnknown) {
				return staticValueUnknown
			}
			return staticValuePresent
		}
		if node.Dispatch != "all" {
			if known && matched {
				return staticValueMissing
			}
			if !known {
				priorUnknown = true
			}
		}
	}
	return staticValueMissing
}

// nodesCurrentVisitSameWave proves co-activation by walking unique selected
// activation parents. Multiple possible parents or a cycle remain unknown: a
// graph-level path alone is not enough to identify the current visit.
func nodesCurrentVisitSameWave(flow model.Workflow, left, right string, inputs map[string]string, visiting map[nodePair]bool) staticValueAvailability {
	if left == right {
		return staticValuePresent
	}
	if left == "" || right == "" || left == flow.Entry || right == flow.Entry {
		return staticValueMissing
	}
	pair := nodePair{left: left, right: right}
	if visiting[pair] {
		return staticValueUnknown
	}
	visiting[pair] = true
	defer delete(visiting, pair)
	leftParents := selectedActivationParents(flow, left, inputs)
	rightParents := selectedActivationParents(flow, right, inputs)
	if len(leftParents) == 0 || len(rightParents) == 0 {
		return staticValueMissing
	}
	if len(leftParents) != 1 || len(rightParents) != 1 {
		return staticValueUnknown
	}
	parents := nodesCurrentVisitSameWave(flow, leftParents[0].node, rightParents[0].node, inputs, visiting)
	return combineStaticAvailability(leftParents[0].availability, rightParents[0].availability, parents)
}

func combineStaticAvailability(values ...staticValueAvailability) staticValueAvailability {
	result := staticValuePresent
	for _, value := range values {
		if value == staticValueMissing {
			return staticValueMissing
		}
		if value == staticValueUnknown {
			result = staticValueUnknown
		}
	}
	return result
}

// A reachable valid external candidate parks the active wave in waiting_input.
// Analyze the whole candidate/fallback chain: a deterministic or possible
// failure in an earlier process candidate can select an external fallback.
func nodeDefersCompletion(root string, flow model.Workflow, source string, inputs map[string]string, executors map[string]driver.Executor, visiting map[string]bool) bool {
	if executors == nil {
		return false
	}
	if visiting == nil {
		visiting = map[string]bool{}
	}
	if visiting[source] {
		return true
	}
	visiting[source] = true
	defer delete(visiting, source)
	routes := []*string{nil}
	if source != flow.Entry {
		if incoming, _ := staticIncomingRoutes(flow, source, inputs); len(incoming) > 0 {
			routes = nil
			for _, id := range incoming {
				id := id
				routes = append(routes, &id)
			}
		}
	}
	for _, incomingSource := range routes {
		if nodeDefersCompletionForRoute(root, flow, source, inputs, incomingSource, executors, visiting) {
			return true
		}
	}
	return false
}

func nodeDefersCompletionForRoute(root string, flow model.Workflow, source string, inputs map[string]string, incomingSource *string, executors map[string]driver.Executor, visiting map[string]bool) bool {
	node := workflowNode(flow, source)
	reachable := true
	maxAttempts := node.Retry.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	attemptCursor := 1
	for index, candidate := range node.Execution.Candidates {
		if !reachable {
			continue
		}
		entry, ok := executors[candidate.Executor]
		if !ok {
			return false
		}
		mode := candidate.Session.Mode
		if mode == "" {
			mode = "new"
		}
		launchPossible := true
		sessionID := ""
		failures := []*model.ExecutionFailure{}
		if mode == "fixed" && candidate.Session.ID != "" {
			resolvedID, known := staticFixedSessionID(flow, node, candidate.Session.ID, inputs)
			if !known {
				failures = append(failures, &model.ExecutionFailure{Kind: "session_incompatible", Executor: candidate.Executor})
				sessionID = "static-existing-session"
			} else if resolvedID == "" {
				failures = append(failures, &model.ExecutionFailure{Kind: "session_incompatible", Executor: candidate.Executor})
				launchPossible = false
			} else {
				sessionID = resolvedID
			}
		} else if mode == "inherit" || mode == "fork" {
			if failure := staticSessionFailureWithStack(root, flow, node, candidate, inputs, executors, map[string]bool{}, incomingSource, nil); failure != nil {
				failures = append(failures, failure)
				launchPossible = false
			} else {
				sessionID = "static-existing-session"
				if sessionFailurePossible(root, flow, node, candidate, inputs, executors, map[string]bool{}, incomingSource, nil) {
					failures = append(failures, &model.ExecutionFailure{Kind: "session_incompatible", Executor: candidate.Executor})
				}
			}
		}
		if launchPossible && entry.Kind == "external" {
			switch {
			case len(entry.Command) > 0:
				failures = append(failures, &model.ExecutionFailure{Kind: "configuration_failed", Executor: candidate.Executor})
				launchPossible = false
			case mode != "new":
				failures = append(failures, &model.ExecutionFailure{Kind: "configuration_failed", Executor: candidate.Executor})
				launchPossible = false
			default:
				return true
			}
		}
		if launchPossible {
			probe := probeCandidateLaunch(root, flow, node, candidate, entry, inputs, mode, sessionID, incomingSource, executors, visiting)
			attempt := attemptCursor + maxAttempts - 1
			if probe.failure == nil {
				failures = append(failures, staticPostLaunchFailures(candidate.Executor, mode, attempt)...)
				if probe.configurationFailurePossible {
					failures = append(failures, &model.ExecutionFailure{Kind: "configuration_failed", Executor: candidate.Executor, Attempt: attempt})
				}
			} else {
				failures = append(failures, probe.preLaunchFailures(candidate.Executor, attempt)...)
			}
			if mode == "fixed" && candidate.Session.ID == "" && possibleRevisit(flow, source, inputs) {
				resumed := probeCandidateLaunch(root, flow, node, candidate, entry, inputs, mode, "static-existing-session", incomingSource, executors, visiting)
				if resumed.failure == nil {
					if resumed.configurationFailurePossible {
						failures = append(failures, &model.ExecutionFailure{Kind: "configuration_failed", Executor: candidate.Executor, Attempt: attempt})
					}
				} else {
					failures = append(failures, resumed.preLaunchFailures(candidate.Executor, attempt)...)
				}
			}
		}
		if index == len(node.Execution.Candidates)-1 {
			return false
		}
		for _, failure := range failures {
			failure.Attempt = attemptCursor + maxAttempts - 1
		}
		mayFallback, known := policy.FallbackMayMatchAny(node.Execution.Fallback, failures)
		reachable = mayFallback || !known
		attemptCursor += maxAttempts
	}
	return false
}

func staticSelectedPath(flow model.Workflow, start, target, stop string, inputs map[string]string) bool {
	seen := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == target {
			return true
		}
		if id == stop || seen[id] || id == "$end" {
			continue
		}
		seen[id] = true
		edges, _ := staticSelectedEdges(flow, id, inputs)
		for _, edge := range edges {
			queue = append(queue, edge.To)
		}
	}
	return false
}

func nodeInputInitializedBefore(flow model.Workflow, source, target string, inputs map[string]string, incomingSource *string) bool {
	return nodeValueAvailability("", flow, source, target, inputs, incomingSource, false, nil, nil) == staticValuePresent
}

func staticSchemaPathTypes(spec map[string]any, path []string) staticJSONTypes {
	missingPossible := false
	for index, part := range path {
		// JSON Schema properties only constrain objects. If this level also
		// accepts null or another non-object value, the schema-valid alternative
		// makes a deeper runtime lookup miss and RenderValue returns nil.
		// Engine inputs and completed results are maps even if their root
		// schema omits type or allows null. Availability is handled above;
		// only nested values can have non-object ancestor alternatives.
		if index > 0 {
			switch staticSchemaObjectConstraint(spec) {
			case staticObjectImpossible:
				return staticOtherType
			case staticObjectMixed:
				missingPossible = true
			}
		}
		properties, _ := spec["properties"].(map[string]any)
		next, exists := properties[part]
		if !exists {
			if additional, exists := spec["additionalProperties"]; exists && additional == false {
				return staticOtherType
			}
			return staticAnyType
		}
		required := false
		switch names := spec["required"].(type) {
		case []any:
			for _, name := range names {
				required = required || name == part
			}
		case []string:
			for _, name := range names {
				required = required || name == part
			}
		}
		missingPossible = missingPossible || !required
		spec, _ = next.(map[string]any)
	}
	types := staticSchemaTypes(spec)
	if missingPossible {
		types |= staticOtherType
	}
	return types
}

type staticObjectConstraint uint8

const (
	staticObjectImpossible staticObjectConstraint = iota
	staticObjectRequired
	staticObjectMixed
)

// staticSchemaObjectConstraint separates schemas that cannot contain objects,
// schemas that require objects, and schemas that allow both. Properties on a
// strict scalar schema are annotations only; runtime cannot traverse them.
func staticSchemaObjectConstraint(spec map[string]any) staticObjectConstraint {
	classify := func(types []string) staticObjectConstraint {
		if len(types) == 0 {
			return staticObjectMixed
		}
		hasObject, hasOther := false, false
		for _, item := range types {
			if item == "object" {
				hasObject = true
			} else {
				hasOther = true
			}
		}
		switch {
		case !hasObject:
			return staticObjectImpossible
		case hasOther:
			return staticObjectMixed
		default:
			return staticObjectRequired
		}
	}
	switch value := spec["type"].(type) {
	case string:
		return classify([]string{value})
	case []any:
		types := make([]string, 0, len(value))
		for _, item := range value {
			name, ok := item.(string)
			if !ok {
				return staticObjectMixed
			}
			types = append(types, name)
		}
		return classify(types)
	case []string:
		return classify(value)
	default:
		return staticObjectMixed
	}
}

func staticSchemaTypes(spec map[string]any) staticJSONTypes {
	typeMask := func(name string) staticJSONTypes {
		switch name {
		case "string":
			return staticStringType
		case "array":
			return staticArrayType
		case "object", "integer", "number", "boolean", "null":
			return staticOtherType
		default:
			return staticAnyType
		}
	}
	switch value := spec["type"].(type) {
	case string:
		return typeMask(value)
	case []any:
		var types staticJSONTypes
		for _, value := range value {
			name, ok := value.(string)
			if !ok {
				return staticAnyType
			}
			types |= typeMask(name)
		}
		if types != 0 {
			return types
		}
	case []string:
		var types staticJSONTypes
		for _, value := range value {
			types |= typeMask(value)
		}
		if types != 0 {
			return types
		}
	}
	return staticAnyType
}

// staticUnresolvedReferences reports values that depend on a future node
// input/result. Workflow inputs, config keys, and the entry node's input are
// fully determined for a representative validation run, including missing
// optional keys which runtime renders as empty.
func staticUnresolvedReferences(value any, context map[string]any, entry bool, inputs map[string]model.InputDefinition) []string {
	unresolved := []string{}
	for _, ref := range template.References(value) {
		if !staticReferenceKnown(context, ref, entry, inputs) {
			unresolved = append(unresolved, ref)
		}
	}
	return unresolved
}

// staticFixedSessionID renders values that are fully determined by supplied
// workflow inputs. Missing optional inputs are known to render as empty at
// runtime; node output and downstream input references remain indeterminate.
func staticFixedSessionID(flow model.Workflow, node model.Node, value string, inputs map[string]string) (string, bool) {
	context := map[string]any{"inputs": inputs}
	if node.ID == flow.Entry {
		context["input"] = inputs
	}
	for _, ref := range template.References(value) {
		if resolved, ok := template.Lookup(context, ref); ok && resolved != nil {
			continue
		}
		if ref == "inputs" || strings.HasPrefix(ref, "inputs.") {
			parts := strings.Split(ref, ".")
			if len(parts) == 2 {
				if definition, declared := flow.Inputs[parts[1]]; declared && definition.Required {
					return "", false
				}
			}
			continue
		}
		if node.ID == flow.Entry && (ref == "input" || strings.HasPrefix(ref, "input.")) {
			continue
		}
		return "", false
	}
	return template.Render(value, context), true
}

func fixedSessionWarnings(name string, executor driver.Executor, candidate model.ExecutorCandidate) []string {
	if candidate.Session.Mode != "fixed" || candidate.Session.ID != "" {
		return nil
	}
	warnings := []string{}
	if (executor.Kind == "command" || executor.Kind == "" || len(executor.Command) > 0) && len(executor.ResumeCommand) == 0 {
		warnings = append(warnings, "a fixed session without an explicit id has no resumeCommand; a resumed visit would fail with configuration_failed")
	}
	if executor.Kind == "codex" && executor.Ephemeral {
		warnings = append(warnings, fmt.Sprintf("a fixed session without an explicit id may resolve to an existing session, which executor %s rejects while ephemeral is enabled", name))
	}
	return warnings
}

// workflowNode returns the definition of a node in the workflow.
func workflowNode(flow model.Workflow, id string) model.Node {
	for _, node := range flow.Nodes {
		if node.ID == id {
			return node
		}
	}
	return model.Node{}
}

func workflowHasNode(flow model.Workflow, id string) bool {
	for _, node := range flow.Nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}

// incomingNodes lists the nodes that can activate a target, including both
// ordinary edges and join predecessors.
func incomingNodes(flow model.Workflow, target string) []string {
	seen := map[string]bool{}
	ids := []string{}
	add := func(id string) {
		if id == "" || id == "$end" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, edge := range flow.Edges {
		if edge.To == target {
			add(edge.From)
		}
	}
	if node := workflowNode(flow, target); node.Join != nil {
		for _, id := range node.Join.From {
			add(id)
		}
	}
	return ids
}

// staticIncomingRoutes returns every immediate predecessor that can activate
// target under the representative inputs. Serial nodes contribute only edges
// that can be selected in order; dispatch-all nodes contribute every edge that
// can match. The boolean is true only when that analysis identifies one
// deterministic incoming route.
func staticIncomingRoutes(flow model.Workflow, target string, inputs map[string]string) ([]string, bool) {
	if flow.Entry == "" || target == "" || target == flow.Entry {
		return nil, false
	}
	reachable := map[string]bool{flow.Entry: true}
	queue := []string{flow.Entry}
	sources := map[string]bool{}
	ordered := []string{}
	resolved := true
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		edges, known := staticSelectedEdges(flow, current, inputs)
		if !known {
			resolved = false
		}
		for _, edge := range edges {
			if edge.To == target {
				if !sources[current] {
					sources[current] = true
					ordered = append(ordered, current)
				}
			}
			if edge.To == "$end" || reachable[edge.To] {
				continue
			}
			reachable[edge.To] = true
			queue = append(queue, edge.To)
		}
	}
	return ordered, resolved && len(ordered) == 1
}

// staticSelectedEdges mirrors runtime edge order for the subset whose values
// are known. Unknown conditions remain possible and make the route unresolved;
// they never get guessed false merely because their runtime data is absent.
func staticSelectedEdges(flow model.Workflow, nodeID string, inputs map[string]string) ([]model.Transition, bool) {
	node := workflowNode(flow, nodeID)
	edges := []model.Transition{}
	for _, edge := range flow.Edges {
		if edge.From == nodeID {
			edges = append(edges, edge)
		}
	}
	selected := []model.Transition{}
	resolved := true
	if node.Dispatch == "all" {
		for _, edge := range edges {
			matched, known := staticConditionMatch(edge.When, flow, nodeID, inputs)
			if !known {
				resolved = false
				selected = append(selected, edge)
				continue
			}
			if matched {
				selected = append(selected, edge)
			}
		}
		return selected, resolved
	}
	for _, edge := range edges {
		matched, known := staticConditionMatch(edge.When, flow, nodeID, inputs)
		if !known {
			resolved = false
			selected = append(selected, edge)
			continue
		}
		if matched {
			selected = append(selected, edge)
			break
		}
	}
	return selected, resolved
}

// staticSourceSession describes what a source node can actually contribute to
// an inherit/fork candidate. It follows the same deterministic launch failure
// and fallback reachability rules as top-level validation, so an unavailable
// command hidden behind an external fallback cannot be treated as a source.
type staticSourceSessionScenario struct {
	route           string
	protocol        string
	protocolKnown   bool
	sessionPossible bool
	alternatives    []staticSourceSessionScenario
}

// staticSourceSessionScenariosWithStack preserves each statically reachable
// activation route. Collapsing incompatible protocols to "unknown", or
// dropping a route that cannot produce a session, would let a compatible route
// hide a deterministic failure on another route.
func staticSourceSessionScenariosWithStack(root string, flow model.Workflow, nodeID string, inputs map[string]string, executors map[string]driver.Executor, visiting map[string]bool) []staticSourceSessionScenario {
	if !workflowHasNode(flow, nodeID) {
		return []staticSourceSessionScenario{{}}
	}
	if visiting[nodeID] {
		return []staticSourceSessionScenario{{sessionPossible: true}}
	}
	node := workflowNode(flow, nodeID)
	routes := []*string{nil}
	if node.ID != flow.Entry && (nodeHasSessionPolicy(node) || nodeHasRouteSensitiveCommandReference(node)) {
		if incoming, _ := staticIncomingRoutes(flow, node.ID, inputs); len(incoming) > 0 {
			routes = nil
			for _, source := range incoming {
				source := source
				routes = append(routes, &source)
			}
		}
	}
	var scenarios []staticSourceSessionScenario
	for _, route := range routes {
		dependency, single := singleExplicitSessionSource(node)
		if !single && route != nil {
			for _, candidate := range node.Execution.Candidates {
				if (candidate.Session.Mode == "inherit" || candidate.Session.Mode == "fork") && candidate.Session.From == "" {
					dependency, single = *route, true
					break
				}
			}
		}
		selections := []map[string]staticSourceSessionScenario{nil}
		if node.ID != flow.Entry && single && workflowHasNode(flow, dependency) {
			// Keep route alternatives through every source layer. The guard is
			// local to expansion; the launch probe adds its own recursion guard.
			ancestors := make(map[string]bool, len(visiting)+1)
			for key, value := range visiting {
				ancestors[key] = value
			}
			ancestors[nodeID] = true
			alternatives := staticSourceSessionScenariosWithStack(root, flow, dependency, inputs, executors, ancestors)
			selections = nil
			for _, alternative := range alternatives {
				selections = append(selections, map[string]staticSourceSessionScenario{dependency: alternative})
			}
		}
		for _, selection := range selections {
			outcomes := staticSourceSessionForRouteWithStack(root, flow, nodeID, inputs, executors, visiting, route, selection)
			// Activation routes remain separate obligations. Candidate outcomes
			// within one route are conditional alternatives, not separate routes
			// that every downstream candidate must handle deterministically.
			scenario := outcomes[0]
			if len(outcomes) > 1 {
				scenario = staticSourceSessionScenario{alternatives: outcomes}
				for _, outcome := range outcomes {
					scenario.sessionPossible = scenario.sessionPossible || outcome.sessionPossible
				}
			}
			if route != nil {
				scenario.route = *route
			}
			scenarios = append(scenarios, scenario)
		}
	}
	return scenarios
}

func staticSourceSessionForRouteWithStack(root string, flow model.Workflow, nodeID string, inputs map[string]string, executors map[string]driver.Executor, visiting map[string]bool, incomingSource *string, selectedSessions map[string]staticSourceSessionScenario) []staticSourceSessionScenario {
	if !workflowHasNode(flow, nodeID) {
		return []staticSourceSessionScenario{{}}
	}
	if visiting[nodeID] {
		// A recursive source chain needs runtime history to bootstrap. Keep the
		// genuinely cyclic case inconclusive rather than recursing forever.
		return []staticSourceSessionScenario{{sessionPossible: true}}
	}
	visiting[nodeID] = true
	defer delete(visiting, nodeID)
	node := workflowNode(flow, nodeID)
	var outcomes []staticSourceSessionScenario
	reachable := true
	maxAttempts := node.Retry.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	attemptCursor := 1
	addProtocol := func(next string) {
		for _, outcome := range outcomes {
			if outcome.protocolKnown && outcome.protocol == next {
				return
			}
		}
		outcomes = append(outcomes, staticSourceSessionScenario{protocol: next, protocolKnown: true, sessionPossible: true})
	}
	addNoSession := func() {
		for _, outcome := range outcomes {
			if !outcome.sessionPossible {
				return
			}
		}
		outcomes = append(outcomes, staticSourceSessionScenario{})
	}

	for index, candidate := range node.Execution.Candidates {
		if !reachable {
			continue
		}
		entry, ok := executors[candidate.Executor]
		if !ok {
			// The registration error is reported elsewhere; do not invent a
			// session failure on top of it.
			return []staticSourceSessionScenario{{sessionPossible: true}}
		}
		mode := candidate.Session.Mode
		if mode == "" {
			mode = "new"
		}
		var possibleFailures []*model.ExecutionFailure
		launchPossible := true
		sessionID := ""
		if mode == "fixed" && candidate.Session.ID != "" {
			resolvedID, known := staticFixedSessionID(flow, node, candidate.Session.ID, inputs)
			if !known {
				// Empty and non-empty rendered IDs are separate runtime branches. The
				// empty branch cannot launch; probe the non-empty branch before it is
				// allowed to contribute a provider session.
				possibleFailures = append(possibleFailures, &model.ExecutionFailure{
					Kind: "session_incompatible", Message: fmt.Sprintf("node %s candidate %s fixed session id may resolve to an empty value", node.ID, candidate.Executor), Executor: candidate.Executor,
				})
				sessionID = "static-existing-session"
			} else if resolvedID == "" {
				possibleFailures = append(possibleFailures, &model.ExecutionFailure{Kind: "session_incompatible", Message: fmt.Sprintf("node %s candidate %s fixed session id resolved to an empty value", node.ID, candidate.Executor), Executor: candidate.Executor})
				launchPossible = false
			} else {
				sessionID = resolvedID
			}
		} else if mode == "inherit" || mode == "fork" {
			if inheritedFailure := staticSessionFailureWithStack(root, flow, node, candidate, inputs, executors, visiting, incomingSource, selectedSessions); inheritedFailure != nil {
				possibleFailures = append(possibleFailures, inheritedFailure)
				launchPossible = false
			} else {
				sessionID = "static-existing-session"
				if sessionFailurePossible(root, flow, node, candidate, inputs, executors, visiting, incomingSource, selectedSessions) {
					possibleFailures = append(possibleFailures, &model.ExecutionFailure{Kind: "session_incompatible", Executor: candidate.Executor})
				}
			}
		}
		// Runtime resolves every session policy before consulting the adapter.
		// Only after a known session succeeds may the external restriction win.
		if launchPossible && entry.Kind == "external" {
			switch {
			case len(entry.Command) > 0:
				possibleFailures = append(possibleFailures, &model.ExecutionFailure{Kind: "configuration_failed", Message: fmt.Sprintf("external executor %s cannot define command", candidate.Executor), Executor: candidate.Executor})
				launchPossible = false
			case mode != "new":
				possibleFailures = append(possibleFailures, &model.ExecutionFailure{Kind: "configuration_failed", Message: fmt.Sprintf("external executor %s does not support session mode %s", candidate.Executor, mode), Executor: candidate.Executor})
				launchPossible = false
			default:
				// External completion has no provider session. Keep this outcome
				// beside successful process protocols, never merge it into them.
				addNoSession()
				reachable = false
				attemptCursor += maxAttempts
				continue
			}
		}
		if launchPossible {
			probe := probeCandidateLaunch(root, flow, node, candidate, entry, inputs, mode, sessionID, incomingSource, executors)
			if probe.failure == nil {
				addProtocol(driver.SessionProtocol(candidate.Executor, entry))
				// New-mode completion does not enforce a session ID. Only
				// fixed/fork enforce it, or inherit already has a nonempty ID.
				if mode == "new" {
					addNoSession()
				}
				if index < len(node.Execution.Candidates)-1 {
					attempt := attemptCursor + maxAttempts - 1
					failures := append(possibleFailures, staticPostLaunchFailures(candidate.Executor, mode, attempt)...)
					if probe.configurationFailurePossible {
						// The runtime may reject a dynamically rendered config.command
						// in Prepare before any process starts. Preserve
						// that fallback route in addition to failures possible after
						// a successful preparation.
						failures = append(failures, &model.ExecutionFailure{
							Kind: "configuration_failed", Started: false, TimedOut: false,
							Executor: candidate.Executor, Attempt: attempt,
						})
					}
					mayFallback, known := policy.FallbackMayMatchAny(node.Execution.Fallback, failures)
					if known {
						reachable = mayFallback
					} else {
						reachable = true
					}
				}
				// A process may still fail at runtime and select a later fallback.
				attemptCursor += maxAttempts
				continue
			}
			possibleFailures = append(possibleFailures, probe.preLaunchFailures(candidate.Executor, attemptCursor+maxAttempts-1)...)
		}
		for _, failure := range possibleFailures {
			failure.Attempt = attemptCursor + maxAttempts - 1
		}
		if index == len(node.Execution.Candidates)-1 {
			reachable = false
			continue
		}
		matched, known := policy.FallbackMayMatchAny(node.Execution.Fallback, possibleFailures)
		if !known {
			reachable = true
		} else {
			reachable = matched
		}
		attemptCursor += maxAttempts
	}
	if len(outcomes) == 0 {
		return []staticSourceSessionScenario{{}}
	}
	return outcomes
}

func nodeHasSessionPolicy(node model.Node) bool {
	for _, candidate := range node.Execution.Candidates {
		mode := candidate.Session.Mode
		if mode == "inherit" || mode == "fork" {
			return true
		}
	}
	return false
}

// A whole nodes.<id>.input reference can change the runtime command shape when
// the referenced node has not been activated. Preserve incoming-route identity
// for these candidates just as session inheritance does.
func nodeHasRouteSensitiveCommandReference(node model.Node) bool {
	wholeNodeInput := func(value any) bool {
		part, ok := value.(string)
		if !ok {
			return false
		}
		if _, remainsText := template.RenderValue(part, map[string]any{}).(string); remainsText {
			return false
		}
		refs := template.References(part)
		if len(refs) != 1 {
			return false
		}
		parts := strings.Split(refs[0], ".")
		return len(parts) >= 3 && parts[0] == "nodes" && (parts[2] == "input" || parts[2] == "result")
	}
	for _, candidate := range node.Execution.Candidates {
		switch command := candidate.Config["command"].(type) {
		case string:
			if wholeNodeInput(command) {
				return true
			}
		case []any:
			for _, part := range command {
				if wholeNodeInput(part) {
					return true
				}
			}
		}
	}
	return false
}

// singleExplicitSessionSource identifies the common explicit source whose
// alternative activation routes can be evaluated as whole candidate-pool
// scenarios. Multiple distinct explicit sources need correlation with runtime
// history, so they remain conservatively inconclusive instead of constructing
// a speculative cross-product.
func singleExplicitSessionSource(node model.Node) (string, bool) {
	source := ""
	for _, candidate := range node.Execution.Candidates {
		mode := candidate.Session.Mode
		if (mode != "inherit" && mode != "fork") || candidate.Session.From == "" {
			continue
		}
		if source == "" {
			source = candidate.Session.From
			continue
		}
		if source != candidate.Session.From {
			return "", false
		}
	}
	return source, source != ""
}

func staticPostLaunchFailures(executor, mode string, attempt int) []*model.ExecutionFailure {
	failure := func(kind string, started, timedOut bool) *model.ExecutionFailure {
		return &model.ExecutionFailure{Kind: kind, Started: started, TimedOut: timedOut, Executor: executor, Attempt: attempt}
	}
	failures := []*model.ExecutionFailure{
		failure("start_failed", false, false),
		failure("timeout", true, true),
		failure("exit_code", true, false),
		failure("result_missing", true, false),
		failure("result_invalid", true, false),
		failure("output_schema", true, false),
	}
	if mode == "fork" || mode == "fixed" {
		failures = append(failures, failure("session_invalid", true, false))
	}
	return failures
}

// staticSessionFailure mirrors engine.sessionForCandidate for the cases static
// validation can decide before workspace availability. When a source could
// still provide a compatible session at runtime it returns nil rather than
// guessing.
func staticSessionFailure(root string, flow model.Workflow, node model.Node, candidate model.ExecutorCandidate, inputs map[string]string, executors map[string]driver.Executor) *model.ExecutionFailure {
	return staticSessionFailureWithStack(root, flow, node, candidate, inputs, executors, map[string]bool{}, nil, nil)
}

func staticSessionFailureForRoute(root string, flow model.Workflow, node model.Node, candidate model.ExecutorCandidate, inputs map[string]string, executors map[string]driver.Executor, incomingSource string, incomingKnown bool, selectedSessions map[string]staticSourceSessionScenario) *model.ExecutionFailure {
	if !incomingKnown {
		return staticSessionFailureWithStack(root, flow, node, candidate, inputs, executors, map[string]bool{}, nil, selectedSessions)
	}
	return staticSessionFailureWithStack(root, flow, node, candidate, inputs, executors, map[string]bool{}, &incomingSource, selectedSessions)
}

func staticSessionFailureWithStack(root string, flow model.Workflow, node model.Node, candidate model.ExecutorCandidate, inputs map[string]string, executors map[string]driver.Executor, visiting map[string]bool, implicitSource *string, selectedSessions map[string]staticSourceSessionScenario) *model.ExecutionFailure {
	mode := candidate.Session.Mode
	if mode == "" {
		mode = "new"
	}
	if mode != "inherit" && mode != "fork" {
		return nil
	}
	failure := func(message string) *model.ExecutionFailure {
		return &model.ExecutionFailure{Kind: "session_incompatible", Started: false, Message: message, Executor: candidate.Executor}
	}
	// The entry node always executes first: no node has produced a session yet
	// on its first visit, so an inherited source cannot exist.
	if node.ID == flow.Entry {
		return failure(fmt.Sprintf("node %s session mode %s needs a session before the entry node runs", node.ID, mode))
	}
	source := candidate.Session.From
	if source != "" && source == node.ID {
		return failure(fmt.Sprintf("node %s session mode %s cannot inherit its own attempt session", node.ID, mode))
	}
	sources := []string{source}
	if source == "" {
		if implicitSource != nil {
			sources = []string{*implicitSource}
		} else {
			sources = incomingNodes(flow, node.ID)
		}
		if len(sources) == 0 {
			// A disconnected node never runs. Do not claim a failure that
			// execution would not reach.
			return nil
		}
	} else {
		// An explicit source must have completed before this visit. Runtime
		// reads the source's current state or an earlier visit from history; an
		// acyclic node that runs only after the target cannot supply one.
		if !edgeReachable(flow, flow.Entry, node.ID, "") {
			// The target itself never runs, so the candidate cannot fail.
			return nil
		}
		if !workflowHasNode(flow, source) {
			return failure(fmt.Sprintf("node %s session mode %s cannot resolve a session from %s because that node does not exist", node.ID, mode, sourceLabel(source)))
		}
		if !edgeReachable(flow, flow.Entry, source, "") {
			return failure(fmt.Sprintf("node %s session mode %s cannot resolve a session from %s because that node never runs", node.ID, mode, sourceLabel(source)))
		}
		// A reachable source is not necessarily complete when the target is
		// activated. In particular, dispatch=all siblings are prepared from the
		// same pre-wave snapshot. completedBefore accounts for ordinary
		// predecessor ordering, join barriers, and the first pass through a
		// cycle; it deliberately does not let a cycle assume its own bootstrap.
		completed := source == flow.Entry || nodeCompletedBefore(flow, source, node.ID)
		if !completed && implicitSource != nil {
			completed = nodeCompletedBeforeOnSelectedRoute(flow, source, node.ID, *implicitSource, inputs)
		}
		if !completed {
			return failure(fmt.Sprintf("node %s session mode %s cannot resolve a session from %s because that node is not guaranteed to complete before node %s is activated", node.ID, mode, sourceLabel(source), node.ID))
		}
	}
	target, targetKnown := executors[candidate.Executor]
	if !targetKnown {
		return nil
	}
	// A selected source-route scenario is checked exactly so fallback is
	// evaluated independently for each route. Without such a selection, retain
	// the conservative rule: any possibly compatible source keeps the result
	// inconclusive rather than guessing a deterministic failure.
	for _, sourceID := range sources {
		if !workflowHasNode(flow, sourceID) {
			continue
		}
		var scenarios []staticSourceSessionScenario
		if selected, ok := selectedSessions[sourceID]; ok {
			scenarios = []staticSourceSessionScenario{selected}
		} else {
			scenarios = staticSourceSessionScenariosWithStack(root, flow, sourceID, inputs, executors, visiting)
		}
		for _, scenario := range scenarios {
			for _, outcome := range sessionOutcomes(scenario) {
				if outcome.sessionPossible && (!outcome.protocolKnown || driver.SessionProtocolCompatible(candidate.Executor, target, outcome.protocol)) {
					return nil
				}
			}
		}
	}
	return failure(fmt.Sprintf("node %s session mode %s cannot resolve a compatible session from %s", node.ID, mode, sourceLabel(sources[0])))
}

func sourceLabel(source string) string {
	if source == "" {
		return "the incoming node"
	}
	return "node " + source
}

// edgeReachable reports whether target is reachable from start by following
// ordinary edges. When skip is set that node is never entered; this models the
// paths that remain if a node is removed from the graph.
func edgeReachable(flow model.Workflow, start, target, skip string) bool {
	if start == "" || target == "" {
		return false
	}
	if start == target {
		return true
	}
	visited := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range flow.Edges {
			if edge.From != current {
				continue
			}
			if edge.To == target {
				return true
			}
			if edge.To == skip || edge.To == "$end" || visited[edge.To] {
				continue
			}
			visited[edge.To] = true
			queue = append(queue, edge.To)
		}
	}
	return false
}

// guaranteedRevisit proves that completing node necessarily activates it
// again through unconditional or statically true routing. Runtime-dependent
// conditions remain inconclusive, and joins are excluded because a path alone
// cannot prove that all of a join's other arrivals will exist.
func guaranteedRevisit(flow model.Workflow, nodeID string, inputs map[string]string) bool {
	if nodeID == "" {
		return false
	}
	nodes := map[string]model.Node{}
	outgoing := map[string][]model.Transition{}
	for _, node := range flow.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range flow.Edges {
		outgoing[edge.From] = append(outgoing[edge.From], edge)
	}
	guaranteed := func(id string) []model.Transition {
		node := nodes[id]
		edges := outgoing[id]
		result := []model.Transition{}
		if node.Dispatch == "all" {
			for _, edge := range edges {
				if matched, known := staticConditionMatch(edge.When, flow, id, inputs); known && matched {
					result = append(result, edge)
				}
			}
		} else {
			// Serial dispatch chooses the first matching edge. It is guaranteed
			// only when every earlier decision is also statically known.
			for _, edge := range edges {
				matched, known := staticConditionMatch(edge.When, flow, id, inputs)
				if !known {
					return nil
				}
				if matched {
					result = append(result, edge)
					break
				}
			}
		}
		return result
	}
	visited := map[string]bool{nodeID: true}
	queue := []string{}
	for _, edge := range guaranteed(nodeID) {
		if edge.To == nodeID {
			return true
		}
		if edge.To == "$end" || nodes[edge.To].Join != nil || visited[edge.To] {
			continue
		}
		visited[edge.To] = true
		queue = append(queue, edge.To)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range guaranteed(current) {
			if edge.To == nodeID {
				return true
			}
			if edge.To == "$end" || nodes[edge.To].Join != nil || visited[edge.To] {
				continue
			}
			visited[edge.To] = true
			queue = append(queue, edge.To)
		}
	}
	return false
}

// staticConditionMatch evaluates the condition subset whose runtime values are
// known from representative inputs. The entry node receives that same object
// as input, and the runtime nodes context exposes it as nodes.<entry>.input.
// Other node input/output and state expressions remain unknown.
func staticConditionMatch(source string, flow model.Workflow, nodeID string, inputs map[string]string) (bool, bool) {
	if source == "" {
		return true, true
	}
	references, err := condition.References(source)
	if err != nil {
		return false, false
	}
	for _, reference := range references {
		if reference.Dynamic || len(reference.Path) == 0 {
			return false, false
		}
		inputPath := -1
		switch reference.Path[0] {
		case "inputs":
			inputPath = 1
		case "input":
			if nodeID != flow.Entry {
				return false, false
			}
			inputPath = 1
		case "nodes":
			if len(reference.Path) < 3 || reference.Path[1] != flow.Entry || reference.Path[2] != "input" {
				return false, false
			}
			inputPath = 3
		default:
			return false, false
		}
		if len(reference.Path) > inputPath {
			key := reference.Path[inputPath]
			if definition, declared := flow.Inputs[key]; declared && definition.Required {
				if _, supplied := inputs[key]; !supplied {
					return false, false
				}
			}
		} else {
			for key, definition := range flow.Inputs {
				if definition.Required {
					if _, supplied := inputs[key]; !supplied {
						return false, false
					}
				}
			}
		}
	}
	entryInput := map[string]any{}
	for key, value := range inputs {
		entryInput[key] = value
	}
	environment := map[string]any{
		"inputs": inputs,
		"nodes": map[string]any{
			flow.Entry: map[string]any{"input": entryInput},
		},
	}
	if nodeID == flow.Entry {
		environment["input"] = entryInput
	}
	matched, err := condition.Evaluate(source, environment)
	return matched, err == nil
}

// nodeCompletedBeforeOnSelectedRoute proves source completion for one
// representative activation route into target. This is narrower than the
// graph-wide dominator proof: a conditional bypass that was not selected must
// not hide a source on the selected branch, while a first activation without
// the source must still fail even if a later cycle could eventually provide it.
func nodeCompletedBeforeOnSelectedRoute(flow model.Workflow, source, target, incoming string, inputs map[string]string) bool {
	if source == "" || target == "" || incoming == "" || source == target {
		return false
	}
	type routeState struct {
		node       string
		seenSource bool
	}
	start := routeState{node: flow.Entry, seenSource: flow.Entry == source}
	queue := []routeState{start}
	visited := map[routeState]bool{start: true}
	found := false
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		edges, _ := staticSelectedEdges(flow, current.node, inputs)
		for _, edge := range edges {
			if edge.To == target && current.node == incoming {
				found = true
				if !current.seenSource {
					return false
				}
			}
			if edge.To == "$end" {
				continue
			}
			next := routeState{node: edge.To, seenSource: current.seenSource || edge.To == source}
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	return found
}

// nodeCompletedBefore reports whether source must have completed before the
// first activation of target. It is a dominator-style fixed point with one
// important graph semantic: a join is activated only after every join.from
// predecessor has completed, so their guaranteed predecessors are combined
// with a union instead of the ordinary alternative-path intersection.
//
// Starting from the greatest set is what makes loops bootstrap correctly. A
// source reached only after target is removed by an entry path that bypasses
// it, while a source on the genuine entry path remains available on later
// visits as well.
func nodeGraphCompletedBefore(flow model.Workflow, source, target string) bool {
	if source == "" || target == "" || source == target || target == flow.Entry {
		return false
	}
	nodes := map[string]model.Node{}
	all := map[string]bool{}
	predecessors := map[string][]string{}
	for _, node := range flow.Nodes {
		nodes[node.ID] = node
		all[node.ID] = true
	}
	if _, ok := nodes[source]; !ok {
		return false
	}
	if _, ok := nodes[target]; !ok {
		return false
	}
	for _, edge := range flow.Edges {
		if edge.To != "$end" {
			predecessors[edge.To] = append(predecessors[edge.To], edge.From)
		}
	}
	completed := map[string]map[string]bool{}
	for id := range nodes {
		completed[id] = map[string]bool{}
		if id == flow.Entry {
			continue
		}
		for candidate := range all {
			if candidate != id {
				completed[id][candidate] = true
			}
		}
	}
	changed := true
	for changed {
		changed = false
		for id, node := range nodes {
			if id == flow.Entry {
				continue
			}
			preds := predecessors[id]
			next := map[string]bool{}
			if len(preds) > 0 {
				if node.Join != nil {
					for _, predecessor := range node.Join.From {
						next[predecessor] = true
						for before := range completed[predecessor] {
							next[before] = true
						}
					}
				} else {
					for candidate := range all {
						if candidate == id {
							continue
						}
						for _, predecessor := range preds {
							if predecessor != candidate && !completed[predecessor][candidate] {
								goto notGuaranteed
							}
						}
						next[candidate] = true
					notGuaranteed:
					}
				}
			}
			delete(next, id)
			if !sameNodeSet(completed[id], next) {
				completed[id] = next
				changed = true
			}
		}
	}
	return completed[target][source]
}

func nodeCompletedBefore(flow model.Workflow, source, target string) bool {
	if source == "" || target == "" || source == target || target == flow.Entry {
		return false
	}
	if nodeGraphCompletedBefore(flow, source, target) {
		return true
	}
	// Parallel execution advances in waves: all runnable tasks prepared in one
	// wave finish before a successor is activated in the next (a failed task
	// stops the run). This makes an unconditional sibling from an earlier wave
	// available even when it is not a graph dominator of the target. Compare
	// against the target's earliest possible wave so a direct/same-wave route
	// can never be mistaken for prior completion.
	sourceWave, guaranteed := guaranteedActivationWave(flow, source)
	targetWave, reachable := minimumActivationWave(flow, target)
	return guaranteed && reachable && sourceWave < targetWave
}

func minimumActivationWave(flow model.Workflow, target string) (int, bool) {
	if flow.Entry == "" || target == "" {
		return 0, false
	}
	distance := map[string]int{flow.Entry: 0}
	queue := []string{flow.Entry}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == target {
			return distance[current], true
		}
		for _, edge := range flow.Edges {
			if edge.From != current || edge.To == "$end" {
				continue
			}
			if _, seen := distance[edge.To]; seen {
				continue
			}
			distance[edge.To] = distance[current] + 1
			queue = append(queue, edge.To)
		}
	}
	return 0, false
}

// guaranteedActivationWave returns the first wave in which a node is certain
// to be activated. Only unconditional fan-out and single unconditional edges
// advance this proof; conditional routing remains indeterminate. Join arrivals
// persist across waves and activate the join only after every predecessor is
// known to have completed.
func guaranteedActivationWave(flow model.Workflow, target string) (int, bool) {
	if flow.Entry == "" || target == "" {
		return 0, false
	}
	nodes := map[string]model.Node{}
	outgoing := map[string][]model.Transition{}
	for _, node := range flow.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range flow.Edges {
		outgoing[edge.From] = append(outgoing[edge.From], edge)
	}
	waves := map[string]int{flow.Entry: 0}
	current := map[string]bool{flow.Entry: true}
	joinArrivals := map[string]map[string]bool{}
	for wave := 0; len(current) > 0 && wave <= len(nodes); wave++ {
		if targetWave, ok := waves[target]; ok && targetWave == wave {
			return wave, true
		}
		next := map[string]bool{}
		for id := range current {
			node := nodes[id]
			edges := outgoing[id]
			guaranteedEdges := []model.Transition{}
			if node.Dispatch == "all" {
				for _, edge := range edges {
					if edge.When == "" {
						guaranteedEdges = append(guaranteedEdges, edge)
					}
				}
			} else if len(edges) == 1 && edges[0].When == "" {
				guaranteedEdges = append(guaranteedEdges, edges[0])
			}
			for _, edge := range guaranteedEdges {
				if edge.To == "$end" {
					continue
				}
				destination := nodes[edge.To]
				if destination.Join == nil {
					if _, seen := waves[edge.To]; !seen {
						waves[edge.To] = wave + 1
						next[edge.To] = true
					}
					continue
				}
				if joinArrivals[edge.To] == nil {
					joinArrivals[edge.To] = map[string]bool{}
				}
				joinArrivals[edge.To][id] = true
				complete := true
				for _, predecessor := range destination.Join.From {
					if !joinArrivals[edge.To][predecessor] {
						complete = false
						break
					}
				}
				if complete {
					if _, seen := waves[edge.To]; !seen {
						waves[edge.To] = wave + 1
						next[edge.To] = true
					}
				}
			}
		}
		current = next
	}
	wave, ok := waves[target]
	return wave, ok
}

func sameNodeSet(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for id := range left {
		if !right[id] {
			return false
		}
	}
	return true
}
