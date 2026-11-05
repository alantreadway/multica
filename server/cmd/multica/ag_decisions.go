package main

// AG patch: `multica decisions push`. ag-jira-sync sends its Decision Desk cards
// and shadow records through the CLI, so the sync never reads the CLI's token.
// See ag-patches/DECISION-DESK-DESIGN.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var decisionsCmd = &cobra.Command{
	Use:   "decisions",
	Short: "Work with the Decision Desk",
}

var decisionsPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Replace the Decision Desk's cards and append its records, from JSON on stdin",
	Long: "Reads {\"cards\": [...], \"records\": [...]} from stdin. The cards are the complete current set: " +
		"an open card that is not listed is resolved. Records are appended, and a repeated id is ignored.",
	Args: cobra.NoArgs,
	RunE: runDecisionsPush,
}

func init() {
	decisionsCmd.AddCommand(decisionsPushCmd)
	rootCmd.AddCommand(decisionsCmd)
}

// agDecisionsPush is the JSON the command reads. Cards has no omitempty on the
// wire: an empty list is a real snapshot that resolves every open card.
type agDecisionsPush struct {
	Cards   *[]json.RawMessage `json:"cards"`
	Records []json.RawMessage  `json:"records"`
}

func runDecisionsPush(cmd *cobra.Command, _ []string) error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	return pushAGDecisions(ctx, client, raw)
}

// pushAGDecisions sends records before cards: a failed card push is retried by
// the next poll, and the repeated records are ignored by id.
func pushAGDecisions(ctx context.Context, client *cli.APIClient, raw []byte) error {
	var push agDecisionsPush
	if err := json.Unmarshal(raw, &push); err != nil {
		return fmt.Errorf("parse stdin: %w", err)
	}
	if push.Cards == nil {
		return fmt.Errorf(`parse stdin: "cards" is required, because a missing list would resolve every card`)
	}
	if len(push.Records) > 0 {
		body := map[string]any{"records": push.Records}
		if err := client.PostJSON(ctx, "/api/ag/decisions/records", body, nil); err != nil {
			return fmt.Errorf("push records: %w", err)
		}
	}
	body := map[string]any{"cards": *push.Cards}
	if err := client.PutJSON(ctx, "/api/ag/decisions/cards", body, nil); err != nil {
		return fmt.Errorf("push cards: %w", err)
	}
	return nil
}
