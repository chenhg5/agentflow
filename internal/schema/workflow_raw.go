package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	schemaassets "github.com/chenhg5/agentflow"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const rawWorkflowLocation = "mem://agentflow/published/workflow.json"

var (
	rawWorkflowOnce      sync.Once
	rawWorkflowSchema    *jsonschema.Schema
	rawWorkflowSchemaErr error
)

func compileRawWorkflow() (*jsonschema.Schema, error) {
	rawWorkflowOnce.Do(func() {
		data, err := schemaassets.ReadSchema("workflow")
		if err != nil {
			rawWorkflowSchemaErr = fmt.Errorf("workflow schema unavailable: %w", err)
			return
		}
		var definition any
		if err := json.Unmarshal(data, &definition); err != nil {
			rawWorkflowSchemaErr = fmt.Errorf("workflow schema is not valid JSON: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		if err := compiler.AddResource(rawWorkflowLocation, definition); err != nil {
			rawWorkflowSchemaErr = err
			return
		}
		rawWorkflowSchema, rawWorkflowSchemaErr = compiler.Compile(rawWorkflowLocation)
	})
	return rawWorkflowSchema, rawWorkflowSchemaErr
}

// ValidateWorkflowJSON validates raw workflow bytes against the published
// workflow.schema.json. Every entry point (CLI, workspace, trigger and web)
// uses this path before typed decoding so the document and the runtime agree
// on structure. In particular, an explicit JSON null is rejected everywhere
// the published schema requires an object, array, string, integer or boolean,
// while nulls nested inside opaque candidate config remain untouched.
func ValidateWorkflowJSON(data []byte) []string {
	compiled, err := compileRawWorkflow()
	if err != nil {
		return []string{err.Error()}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return []string{fmt.Sprintf("invalid workflow JSON: %v", err)}
	}
	// A workflow file must contain exactly one JSON document. The second decode
	// must report EOF; otherwise trailing tokens or a second document would be
	// silently ignored by every entry point that shares this validator.
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return []string{"invalid workflow JSON: multiple JSON documents"}
		}
		return []string{fmt.Sprintf("invalid workflow JSON: %v", err)}
	}
	if err := compiled.Validate(value); err != nil {
		if validation, ok := err.(*jsonschema.ValidationError); ok {
			var messages []string
			collectValidationErrors(validation, &messages)
			if len(messages) > 0 {
				return messages
			}
		}
		return []string{err.Error()}
	}
	return nil
}
