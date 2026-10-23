package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// AG patch: deferred retry until quota reset (see ag-patches/PATCHES.md).
//
// provider_quota_limit joins the auto-retry path as a deferred retry whose
// fire_at is the provider's reset time, so a task killed by a usage-window
// limit comes back on its own. Hooks live in task.go (retryableReasons,
// retryAttemptCeiling, retryDelayForAttempt, retryEligible and the two
// fire_at sites) and in the daemon fail handler.

const (
	agQuotaMaxAttempts = 3
	// agQuotaResetSlack lets the window actually reopen before the retry fires.
	agQuotaResetSlack = 2 * time.Minute
	// agQuotaUnknownResetDelay is the wait when no reset time is known. The
	// retry re-evaluates on its own failure, so a wrong guess costs one attempt.
	agQuotaUnknownResetDelay = time.Hour
	// A reset further out than this is more likely a mis-parse than a real
	// window, so it counts as unknown.
	agQuotaMaxResetHorizon = 8 * 24 * time.Hour
)

var agQuotaReason = string(taskfailure.ReasonAgentProviderQuotaLimit)

type agRetryNotBeforeKey struct{}

// AGWithRetryNotBefore carries the daemon's ag_retry_not_before (RFC 3339) from
// the fail handler to FailTaskWithTransition without widening its signature.
func AGWithRetryNotBefore(ctx context.Context, notBefore string) context.Context {
	if notBefore == "" {
		return ctx
	}
	return context.WithValue(ctx, agRetryNotBeforeKey{}, notBefore)
}

func agRetryNotBefore(ctx context.Context) (time.Time, bool) {
	s, _ := ctx.Value(agRetryNotBeforeKey{}).(string)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// agQuotaFailureReason makes a daemon-reported reset time authoritative: the
// patched daemon only sends it for a window limit, whatever the text says (the
// classifier would send "token limit" text to context_overflow).
func agQuotaFailureReason(ctx context.Context, reason string) string {
	if _, ok := agRetryNotBefore(ctx); ok {
		return agQuotaReason
	}
	return reason
}

// agQuotaRetryDelay is the retryDelayForAttempt answer for a quota failure. It
// is the unknown-reset wait; agQuotaFireAt refines it where the error text is
// at hand.
func agQuotaRetryDelay(reason string) (time.Duration, bool) {
	if reason != agQuotaReason {
		return 0, false
	}
	return agQuotaUnknownResetDelay, true
}

// agQuotaRetryCeiling is the retryAttemptCeiling answer for a quota failure.
func agQuotaRetryCeiling(reason string, taskMaxAttempts int32) (int32, bool) {
	if reason != agQuotaReason || taskMaxAttempts >= agQuotaMaxAttempts {
		return 0, false
	}
	return agQuotaMaxAttempts, true
}

// agQuotaRetryBlocked reports a quota failure whose text is billing, which
// waiting does not fix.
func agQuotaRetryBlocked(reason, errText string) bool {
	return reason == agQuotaReason && taskfailure.AGIsBillingFailure(errText)
}

// agQuotaFireAt returns when a quota retry should fire: the reset time (from
// the daemon, else the error text) plus a short slack, or an hour from now when
// no usable reset is known. Reasons other than quota keep fireAt unchanged.
func agQuotaFireAt(ctx context.Context, reason, errText string, now time.Time, fireAt time.Time) time.Time {
	if reason != agQuotaReason {
		return fireAt
	}
	reset, ok := agRetryNotBefore(ctx)
	if !ok {
		reset, ok = taskfailure.AGQuotaResetTime(errText, now, time.Local)
	}
	if !ok || reset.After(now.Add(agQuotaMaxResetHorizon)) {
		return now.Add(agQuotaUnknownResetDelay)
	}
	if reset.Before(now) {
		reset = now
	}
	return reset.Add(agQuotaResetSlack)
}

// agWithFailText gives retryEligible the failure text of the attempt being
// failed: FailTask decides on the retry before the row's error is written.
func agWithFailText(t db.AgentTaskQueue, errMsg string) db.AgentTaskQueue {
	t.Error = pgtype.Text{String: errMsg, Valid: true}
	return t
}

// Registered here, not in the retryableReasons literal, so the table's
// alignment (and every rebase of it) stays untouched.
func init() { retryableReasons[agQuotaReason] = true }
