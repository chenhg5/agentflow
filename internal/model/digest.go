package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Digest identifies a workflow definition by content. It hashes the parsed
// definition, so formatting changes keep the same digest while any change to
// nodes, contracts, or inputs produces a new one.
func Digest(workflow Workflow) string {
	data, err := json.Marshal(workflow)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

// WorkflowDigest returns the digest recorded when the run was created, or
// derives it from the run's workflow snapshot for runs created before digests
// were recorded.
func (r *Run) WorkflowDigest() string {
	if r.State.WorkflowDigest != "" {
		return r.State.WorkflowDigest
	}
	return Digest(r.Workflow)
}
