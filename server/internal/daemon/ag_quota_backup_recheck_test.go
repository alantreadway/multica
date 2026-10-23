package daemon

// AG patch: force-recheck hints (upstream #7452) on the backup claim. The
// Claude half is filtered to urgent issues, so it must not carry or acknowledge
// a Claude runtime's hint; the other runtimes' unfiltered claim must.

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"testing"
)

// agEchoIdleHints answers a batch claim with no tasks and acknowledges every
// hinted runtime in its own runtime_ids, as the server does for an idle scan.
func agEchoIdleHints(body map[string]any) string {
	listed := map[string]bool{}
	for _, id := range body["runtime_ids"].([]any) {
		listed[id.(string)] = true
	}
	acked := []string{}
	if hints, ok := body["force_recheck_runtime_ids"].([]any); ok {
		for _, id := range hints {
			if listed[id.(string)] {
				acked = append(acked, id.(string))
			}
		}
	}
	out, _ := json.Marshal(map[string]any{"tasks": []any{}, "force_rechecked_runtime_ids": acked})
	return string(out)
}

func agClaimHints(body map[string]any) []string {
	var out []string
	if hints, ok := body["force_recheck_runtime_ids"].([]any); ok {
		for _, id := range hints {
			out = append(out, id.(string))
		}
	}
	return out
}

func TestAGPatchBackupClaimForceRecheck(t *testing.T) {
	runtimes := []string{"rt-claude", "rt-codex", "rt-claude-b"}
	hinted := []string{"rt-claude", "rt-codex"}

	acked := func(t *testing.T, res claimTasksResult) []string {
		t.Helper()
		if res.ForceRecheckedRuntimeIDs == nil {
			t.Fatal("acknowledgement is nil, which drops every drained hint")
		}
		got := append([]string(nil), *res.ForceRecheckedRuntimeIDs...)
		sort.Strings(got)
		return got
	}

	t.Run("AG patch: only the other runtimes' hints are sent and acknowledged", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.srv.reply = agEchoIdleHints
		f.ready(t)
		f.holdPrimary(t)

		res, err := f.d.agQuotaClaim(context.Background(), "daemon-test", runtimes, 5, hinted...)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		bodies := f.srv.bodies()
		if len(bodies) != 2 {
			t.Fatalf("claims = %+v, want the urgent Claude claim and the Codex fill", claimShapes(t, f.srv))
		}
		for _, body := range bodies {
			if slices.Contains(agClaimHints(body), "rt-claude") {
				t.Fatalf("claim %v carries the Claude runtime's hint", body)
			}
		}
		if got := agClaimHints(bodies[1]); !reflect.DeepEqual(got, []string{"rt-codex"}) {
			t.Fatalf("Codex claim hints = %v, want [rt-codex]", got)
		}
		if got := acked(t, res); !reflect.DeepEqual(got, []string{"rt-codex"}) {
			t.Fatalf("acknowledged = %v, want only rt-codex, so rt-claude stays forced", got)
		}
	})

	t.Run("AG patch: at the backup's limit the Codex-only claim still sends its hint", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.srv.reply = agEchoIdleHints
		f.ready(t)
		f.holdPrimary(t)
		f.d.agPace.mu.Lock()
		f.d.agPace.claimed = map[string]struct{}{"c1": {}, "c2": {}}
		f.d.agPace.mu.Unlock()

		res, err := f.d.agQuotaClaim(context.Background(), "daemon-test", runtimes, 5, hinted...)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		bodies := f.srv.bodies()
		if len(bodies) != 1 || !reflect.DeepEqual(agClaimHints(bodies[0]), []string{"rt-codex"}) {
			t.Fatalf("claims = %+v, want one Codex claim hinting rt-codex", bodies)
		}
		if got := acked(t, res); !reflect.DeepEqual(got, []string{"rt-codex"}) {
			t.Fatalf("acknowledged = %v, want only rt-codex", got)
		}
	})

	t.Run("AG patch: with no other runtime nothing is claimed and nothing acknowledged", func(t *testing.T) {
		f := newAGBackupFixture(t)
		f.srv.reply = agEchoIdleHints
		f.ready(t)
		f.holdPrimary(t)
		f.d.agPace.mu.Lock()
		f.d.agPace.claimed = map[string]struct{}{"c1": {}, "c2": {}}
		f.d.agPace.mu.Unlock()

		res, err := f.d.agQuotaClaim(context.Background(), "daemon-test", []string{"rt-claude"}, 5, "rt-claude")
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if n := len(f.srv.bodies()); n != 0 {
			t.Fatalf("claims = %d, want none", n)
		}
		if got := acked(t, res); len(got) != 0 {
			t.Fatalf("acknowledged = %v, want none", got)
		}
	})
}
