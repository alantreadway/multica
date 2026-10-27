package handler

// These need the test database and are not run by ag-patches/test.sh; see its header.

import (
	"net/http"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/agtokenrate"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestAGPatchTokenRateHeartbeat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	prev := agTokenRateBoard
	agTokenRateBoard = &agtokenrate.Board{}
	t.Cleanup(func() { agTokenRateBoard = prev })

	var mu sync.Mutex
	var published []agtokenrate.Rate
	testHandler.Bus.Subscribe(protocol.EventAGRuntimeTokenRate, func(e events.Event) {
		if rate, ok := e.Payload.(agtokenrate.Rate); ok && e.WorkspaceID == testWorkspaceID {
			mu.Lock()
			published = append(published, rate)
			mu.Unlock()
		}
	})
	publishedFor := func(runtimeID string) []agtokenrate.Rate {
		mu.Lock()
		defer mu.Unlock()
		var out []agtokenrate.Rate
		for _, r := range published {
			if r.RuntimeID == runtimeID {
				out = append(out, r)
			}
		}
		return out
	}
	heartbeat := func(t *testing.T, runtimeID string, rate map[string]any) {
		t.Helper()
		body := map[string]any{"runtime_id": runtimeID}
		if rate != nil {
			body["ag_token_rate"] = rate
		}
		testutil.Call(t, testHandler.DaemonHeartbeat, newOwnerDaemonRequest(http.MethodPost, "/api/daemon/heartbeat", body)).Want(http.StatusOK)
	}

	t.Run("AG patch: an HTTP heartbeat publishes the rate and the list serves it", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		heartbeat(t, runtimeID, map[string]any{"input_per_sec": 12.5, "output_per_sec": 80, "cache_read_per_sec": 900, "window_seconds": 60, "active_runs": 1})
		got := publishedFor(runtimeID)
		if len(got) != 1 || got[0].OutputPerSec != 80 || got[0].ActiveRuns != 1 {
			t.Fatalf("published = %+v", got)
		}

		var list []agtokenrate.Rate
		testutil.Call(t, testHandler.ListAGTokenRates, newRequest(http.MethodGet, "/api/runtimes/ag-token-rates", nil)).Want(http.StatusOK).JSON(&list)
		found := false
		for _, r := range list {
			found = found || (r.RuntimeID == runtimeID && r.InputPerSec == 12.5)
		}
		if !found {
			t.Fatalf("list = %+v, want %s", list, runtimeID)
		}
	})

	t.Run("AG patch: a stock or malformed heartbeat publishes nothing", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		heartbeat(t, runtimeID, nil)
		heartbeat(t, runtimeID, map[string]any{"output_per_sec": -5, "window_seconds": 60})
		if got := publishedFor(runtimeID); len(got) != 0 {
			t.Fatalf("published = %+v", got)
		}
	})
}
