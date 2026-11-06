package cli

import (
	"context"
	"fmt"

	"github.com/chenhg5/agentflow/internal/store"
)

func resourceCommand(args []string, root string) error {
	if len(args) == 0 || isHelp(args[0]) {
		fmt.Println("Usage: agentflow resource list | resolve <claim-id> --confirm-stopped --reason <evidence> [--format json]")
		return nil
	}
	parsed, err := parseArgs(args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		snapshot, err := store.ResourceStatus(context.Background(), root)
		if err != nil {
			return internalError(err)
		}
		return writeData(snapshot, parsed.Output())
	case "resolve":
		if len(parsed.Positionals) != 1 {
			return usageError("claim ID is required", "Use resource list to inspect orphaned claims.", "resource_claim")
		}
		if parsed.DryRun {
			return usageError("resolve does not support dry-run", "Inspect with resource list.", "resource_claim")
		}
		if err := store.ResolveResource(context.Background(), root, parsed.Positionals[0], parsed.Reason, parsed.ConfirmStopped); err != nil {
			return conflictError(err.Error(), "Verify the complete execution scope has stopped before cleanup.", parsed.Format)
		}
		return writeData(map[string]any{"ok": true, "claim_id": parsed.Positionals[0], "status": "released"}, parsed.Output())
	default:
		return usageError("unknown resource command", "Use resource list or resource resolve.", "resource_action")
	}
}
