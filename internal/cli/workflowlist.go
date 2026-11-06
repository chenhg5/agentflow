package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
)

// workflowList shows the workspace's workflows with what people need to tell
// them apart: title, description, tags and group. It reads the files the
// workspace layout puts workflows in, workflow.json and workflows/*.json.
func workflowList(parsed parsedArgs) error {
	cwd, err := os.Getwd()
	if err != nil {
		return internalError(err)
	}
	files, err := workspaceWorkflowFiles(cwd)
	if err != nil {
		return internalError(err)
	}
	items := make([]map[string]any, 0, len(files))
	for _, file := range files {
		relative, _ := filepath.Rel(cwd, file)
		item := map[string]any{"file": filepath.ToSlash(relative)}
		workflow, err := readWorkflow(file, parsed.Format)
		if err == nil {
			if problems := schema.Validate(workflow); len(problems) > 0 {
				err = fmt.Errorf("%s", strings.Join(problems, "; "))
			}
		}
		if err != nil {
			if !labelsMatch(workflow, parsed.Tags, parsed.Group) {
				continue
			}
			item["valid"] = false
			item["error"] = errorMessage(err)
			if workflow.Name != "" {
				item["name"] = workflow.Name
			}
			items = append(items, item)
			continue
		}
		if !labelsMatch(workflow, parsed.Tags, parsed.Group) {
			continue
		}
		item["valid"] = true
		item["name"] = workflow.Name
		for key, value := range workflowLabels(workflow) {
			item[key] = value
		}
		item["nodes"] = len(workflow.Nodes)
		items = append(items, item)
	}
	if parsed.Output().resolved() == "table" {
		return printWorkflowTable(items)
	}
	return writeData(map[string]any{"ok": true, "workflows": items}, parsed.Output())
}

// workspaceWorkflowFiles returns workflow.json, then workflows/*.json sorted.
func workspaceWorkflowFiles(root string) ([]string, error) {
	var files []string
	if info, err := os.Stat(filepath.Join(root, "workflow.json")); err == nil && !info.IsDir() {
		files = append(files, filepath.Join(root, "workflow.json"))
	}
	more, err := filepath.Glob(filepath.Join(root, "workflows", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(more)
	return append(files, more...), nil
}

// workflowLabels is the people-facing part of a workflow, keyed as every
// command prints it.
func workflowLabels(workflow model.Workflow) map[string]any {
	return map[string]any{
		"title":       workflow.Label(),
		"description": workflow.Description,
		"tags":        nonNilTags(workflow.Tags),
		"group":       workflow.Group,
	}
}

func nonNilTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

// labelsMatch reports whether workflow has every tag in tags and, when group
// is set, belongs to that group.
func labelsMatch(workflow model.Workflow, tags []string, group string) bool {
	if group != "" && workflow.Group != group {
		return false
	}
	for _, want := range tags {
		found := false
		for _, tag := range workflow.Tags {
			found = found || tag == want
		}
		if !found {
			return false
		}
	}
	return true
}

func errorMessage(err error) string {
	if commandErr, ok := err.(*CommandError); ok {
		return commandErr.Message
	}
	return err.Error()
}

func printWorkflowTable(items []map[string]any) error {
	byGroup := map[string][]map[string]any{}
	var groups []string
	for _, item := range items {
		group, _ := item["group"].(string)
		if _, seen := byGroup[group]; !seen {
			groups = append(groups, group)
		}
		byGroup[group] = append(byGroup[group], item)
	}
	sort.Slice(groups, func(i, j int) bool {
		if (groups[i] == "") != (groups[j] == "") {
			return groups[j] == "" // ungrouped last
		}
		return groups[i] < groups[j]
	})
	for index, group := range groups {
		if index > 0 {
			fmt.Println()
		}
		heading := group
		if heading == "" {
			heading = "(no group)"
		}
		fmt.Println(heading)
		for _, item := range byGroup[group] {
			if item["valid"] == false {
				fmt.Printf("  %v  INVALID  %v\n", item["file"], item["error"])
				continue
			}
			line := fmt.Sprintf("  %v", item["title"])
			if item["title"] != item["name"] {
				line += fmt.Sprintf(" (%v)", item["name"])
			}
			if tags, _ := item["tags"].([]string); len(tags) > 0 {
				line += "  #" + strings.Join(tags, " #")
			}
			fmt.Println(line)
			if description, _ := item["description"].(string); description != "" {
				fmt.Printf("    %s\n", strings.ReplaceAll(description, "\n", " "))
			}
		}
	}
	return nil
}
