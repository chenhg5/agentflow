package graph

import (
	"fmt"

	"github.com/chenhg5/agentflow/internal/model"
)

const (
	ChannelControl   = "control"
	ChannelCondition = "condition"
)

type Edge struct {
	ID            string   `json:"id"`
	Source        string   `json:"source"`
	Target        string   `json:"target"`
	Channels      []string `json:"channels"`
	Condition     string   `json:"condition,omitempty"`
	MaxIterations int      `json:"maxIterations,omitempty"`
}

type Document struct {
	Edges []Edge `json:"edges"`
}

func Describe(workflow model.Workflow) Document {
	result := make([]Edge, 0, len(workflow.Edges))
	for index, transition := range workflow.Edges {
		channels := []string{ChannelControl}
		if transition.When != "" {
			channels = append(channels, ChannelCondition)
		}
		result = append(result, Edge{
			ID: fmt.Sprintf("edge-%d", index), Source: transition.From, Target: transition.To,
			Channels: channels, Condition: transition.When,
			MaxIterations: transition.MaxIterations,
		})
	}
	return Document{Edges: result}
}

func Validate(workflow model.Workflow) []string { return validateTransitions(workflow) }
