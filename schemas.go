package agentflow

import "embed"

// Schema documents travel with the binary so discovery works in a new project.
//
//go:embed workflow.schema.json trigger.schema.json executors.schema.json resources.schema.json
var schemas embed.FS

func ReadSchema(name string) ([]byte, error) { return schemas.ReadFile(name + ".schema.json") }
