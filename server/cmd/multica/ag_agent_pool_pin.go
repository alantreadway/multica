package main

// AG patch: runtime pool. `multica agent update <id> --ag-pool-pinned` keeps an
// agent out of the pool (only its own runtime claims its tasks) by setting
// runtime_config.ag_pool_pinned. The server replaces runtime_config wholesale,
// so the flag merges into the agent's current runtime_config (or into
// --runtime-config when both are given). See internal/agpool.

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/agpool"
	"github.com/multica-ai/multica/server/internal/cli"
)

func init() {
	agentUpdateCmd.Flags().Bool("ag-pool-pinned", false, "Runtime pool (MULTICA_AG_POOL): only the agent's own runtime may claim its tasks; pool peers skip them. --ag-pool-pinned=false unpins. Kept in runtime_config.ag_pool_pinned; other runtime_config keys are kept.")
}

func agApplyPoolPinnedFlag(cmd *cobra.Command, client *cli.APIClient, agentID string, body map[string]any) error {
	if !cmd.Flags().Changed("ag-pool-pinned") {
		return nil
	}
	pinned, _ := cmd.Flags().GetBool("ag-pool-pinned")
	var rc map[string]any
	if v, ok := body["runtime_config"]; ok {
		m, isObject := v.(map[string]any)
		if !isObject && v != nil {
			return fmt.Errorf("--ag-pool-pinned needs --runtime-config to be a JSON object")
		}
		rc = m
	} else {
		var agent struct {
			RuntimeConfig map[string]any `json:"runtime_config"`
		}
		ctx, cancel := cli.APIContext(context.Background())
		defer cancel()
		if err := client.GetJSON(ctx, "/api/agents/"+agentID, &agent); err != nil {
			return fmt.Errorf("get agent: %w", err)
		}
		rc = agent.RuntimeConfig
	}
	body["runtime_config"] = agpool.SetPinned(rc, pinned)
	return nil
}
