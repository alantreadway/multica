package main

// AG patch: `multica decisions push`. ag-jira-sync sends its Decision Desk cards
// and shadow records through the CLI, so the sync never reads the CLI's token.
// See ag-patches/DECISION-DESK-DESIGN.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
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

var decisionsClaimCmd = &cobra.Command{
	Use:   "claim",
	Short: "Claim the oldest queued Decision Desk action, printing it as JSON",
	Long:  "Prints {\"intent\": ..., \"card\": ..., \"card_open\": ...}, with a null intent when nothing is queued. Each call also counts as the desk worker's heartbeat.",
	Args:  cobra.NoArgs,
	RunE:  runDecisionsClaim,
}

var decisionsResultCmd = &cobra.Command{
	Use:   "result <intent-id>",
	Short: "Report what happened to a claimed Decision Desk action, from JSON on stdin",
	Long:  "Reads {\"status\": \"succeeded|failed|refused\", \"message\": ..., \"record\": ...} from stdin.",
	Args:  cobra.ExactArgs(1),
	RunE:  runDecisionsResult,
}

func init() {
	decisionsCmd.AddCommand(decisionsPushCmd, decisionsClaimCmd, decisionsResultCmd)
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

func runDecisionsClaim(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	return claimAGDecision(ctx, client, cmd.OutOrStdout())
}

func claimAGDecision(ctx context.Context, client *cli.APIClient, out io.Writer) error {
	var claim json.RawMessage
	if err := client.PostJSON(ctx, "/api/ag/decisions/intents/claim", map[string]any{}, &claim); err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	_, err := fmt.Fprintln(out, string(claim))
	return err
}

func runDecisionsResult(cmd *cobra.Command, args []string) error {
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
	return reportAGDecision(ctx, client, args[0], raw)
}

func reportAGDecision(ctx context.Context, client *cli.APIClient, id string, raw []byte) error {
	if !json.Valid(raw) {
		return fmt.Errorf("parse stdin: not JSON")
	}
	path := "/api/ag/decisions/intents/" + url.PathEscape(id) + "/result"
	if err := client.PostJSON(ctx, path, json.RawMessage(raw), nil); err != nil {
		return fmt.Errorf("report result: %w", err)
	}
	return nil
}
