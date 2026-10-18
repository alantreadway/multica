package handler

// These need the test database and are not run by ag-patches/test.sh; see its header.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agquota"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func agQuotaReport(util float64) map[string]any {
	return map[string]any{
		"account": "primary", "effective_cap": 3, "max": 5,
		"windows": map[string]any{"five_hour": map[string]any{"utilization": util, "resets_at": "2026-10-18T14:00:00Z", "observed_at": "2026-10-18T12:00:00Z"}},
		"backup":  map[string]any{"detected": false, "valid": false, "reason": "", "enabled": false, "max_concurrent": 2}, "backup_windows": map[string]any{},
	}
}

func agQuotaHeartbeat(t *testing.T, runtimeID string, body map[string]any) map[string]any {
	t.Helper()
	body["runtime_id"] = runtimeID
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/heartbeat", body, testWorkspaceID, "runtime-local-skills-daemon")
	var ack map[string]any
	testutil.Call(t, testHandler.DaemonHeartbeat, req).Want(http.StatusOK).JSON(&ack)
	return ack
}

func agQuotaPutSettings(actorID, runtimeID string, body map[string]any) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := newRequestAs(actorID, http.MethodPut, "/api/runtimes/"+runtimeID+"/ag-settings", body)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.UpdateAGRuntimeSettings(w, req)
	return w
}

func agQuotaUpdatedAt(t *testing.T, runtimeID string) time.Time {
	t.Helper()
	var at time.Time
	if err := testPool.QueryRow(context.Background(), `SELECT updated_at FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestAGPatchQuotaHeartbeatMetadata(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("AG patch: HTTP heartbeat stores and updates the quota report beside other metadata", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		if _, err := testPool.Exec(context.Background(),
			`UPDATE agent_runtime SET metadata = '{"cli_version":"v0.6.2"}'::jsonb WHERE id = $1`, runtimeID); err != nil {
			t.Fatal(err)
		}

		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.5)})
		md := agDrainMetadata(t, runtimeID)
		var got protocol.DaemonQuotaReport
		if err := json.Unmarshal(md[agquota.MetadataKey], &got); err != nil || got.Max != 5 || *got.Windows["five_hour"].Utilization != 0.5 {
			t.Fatalf("ag_quota = %s (%v)", md[agquota.MetadataKey], err)
		}
		if string(md["cli_version"]) != `"v0.6.2"` {
			t.Fatalf("other metadata lost: %v", md)
		}

		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.62)})
		_ = json.Unmarshal(agDrainMetadata(t, runtimeID)[agquota.MetadataKey], &got)
		if *got.Windows["five_hour"].Utilization != 0.62 {
			t.Fatalf("after the second report: %+v", got)
		}
	})

	t.Run("AG patch: an unchanged report does not rewrite the row and a changed one publishes a refresh", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		var mu sync.Mutex
		refreshes := 0
		testHandler.Bus.Subscribe(protocol.EventDaemonRegister, func(e events.Event) {
			payload, _ := e.Payload.(map[string]any)
			if payload["action"] == "quota" && e.WorkspaceID == testWorkspaceID {
				mu.Lock()
				refreshes++
				mu.Unlock()
			}
		})
		count := func() int {
			mu.Lock()
			defer mu.Unlock()
			return refreshes
		}

		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.5)})
		before := agQuotaUpdatedAt(t, runtimeID)
		time.Sleep(20 * time.Millisecond)
		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.5)})
		if after := agQuotaUpdatedAt(t, runtimeID); !after.Equal(before) {
			t.Fatalf("updated_at moved %s -> %s on an unchanged report", before, after)
		}
		if got := count(); got != 1 {
			t.Fatalf("refreshes after an unchanged report = %d, want 1", got)
		}
		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.6)})
		if got := count(); got != 2 {
			t.Fatalf("refreshes after a changed report = %d, want 2", got)
		}

		moved := agQuotaReport(0.6)
		moved["windows"].(map[string]any)["five_hour"].(map[string]any)["observed_at"] = "2026-10-18T12:01:00Z"
		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": moved})
		if got := count(); got != 2 {
			t.Fatalf("refreshes after an observed_at-only change = %d, want still 2", got)
		}
		var stored protocol.DaemonQuotaReport
		_ = json.Unmarshal(agDrainMetadata(t, runtimeID)[agquota.MetadataKey], &stored)
		if stored.Windows["five_hour"].ObservedAt != "2026-10-18T12:01:00Z" {
			t.Fatalf("observed_at-only change was not written: %+v", stored.Windows)
		}
	})

	t.Run("AG patch: a heartbeat without the field leaves a stored report alone", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.5)})
		agQuotaHeartbeat(t, runtimeID, map[string]any{"supports_batch_import": true})
		if _, present := agDrainMetadata(t, runtimeID)[agquota.MetadataKey]; !present {
			t.Fatal("a stock heartbeat cleared the quota report")
		}
	})

	t.Run("AG patch: a malformed report is not written", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		for _, bad := range []map[string]any{
			{"account": "elsewhere", "effective_cap": 1, "max": 1},
			{"account": "primary", "effective_cap": -3, "max": 1},
			{"account": "primary", "effective_cap": 1, "max": -1},
		} {
			agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": bad})
		}
		if _, present := agDrainMetadata(t, runtimeID)[agquota.MetadataKey]; present {
			t.Fatal("a malformed report was stored")
		}
	})

	t.Run("AG patch: the report and the drain report both land from one heartbeat", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		agQuotaHeartbeat(t, runtimeID, map[string]any{
			"ag_quota": agQuotaReport(0.5),
			"ag_drain": map[string]any{"draining": true, "idle": false, "active_tasks": 1},
		})
		md := agDrainMetadata(t, runtimeID)
		if _, ok := md[agquota.MetadataKey]; !ok {
			t.Fatalf("no ag_quota: %v", md)
		}
		if _, ok := md["ag_drain"]; !ok {
			t.Fatalf("no ag_drain: %v", md)
		}
	})
}

func TestAGPatchRuntimeSettings(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("AG patch: PUT stores the setting and the HTTP ack returns it", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		if ack := agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.5)}); ack["ag_settings"] != nil {
			t.Fatalf("ack carries a setting before one is stored: %v", ack)
		}

		w := agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": true, "backup_max_concurrent": 4})
		if w.Code != http.StatusOK {
			t.Fatalf("PUT = %d %s", w.Code, w.Body)
		}
		ack := agQuotaHeartbeat(t, runtimeID, map[string]any{"supports_batch_import": true})
		got, _ := ack["ag_settings"].(map[string]any)
		if got["backup_enabled"] != true || got["backup_max_concurrent"] != float64(4) {
			t.Fatalf("ack ag_settings = %v", ack["ag_settings"])
		}
		if s := agquota.ParseSettings(agDrainMetadata(t, runtimeID)[agquota.SettingsKey]); s == nil || !s.BackupEnabled || s.BackupMaxConcurrent != 4 {
			t.Fatalf("stored setting = %+v", s)
		}
	})

	t.Run("AG patch: an unset limit defaults to 2 and a repeat PUT does not rewrite the row", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		var resp protocol.AGRuntimeSettings
		w := agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": false})
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil || resp.BackupMaxConcurrent != 2 || resp.BackupEnabled {
			t.Fatalf("PUT = %d %s", w.Code, w.Body)
		}
		before := agQuotaUpdatedAt(t, runtimeID)
		time.Sleep(20 * time.Millisecond)
		agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": false})
		if after := agQuotaUpdatedAt(t, runtimeID); !after.Equal(before) {
			t.Fatalf("updated_at moved on an identical PUT: %s -> %s", before, after)
		}
	})

	t.Run("AG patch: PUT publishes an ag_settings refresh only when the setting changed", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		var mu sync.Mutex
		refreshes := 0
		testHandler.Bus.Subscribe(protocol.EventDaemonRegister, func(e events.Event) {
			payload, _ := e.Payload.(map[string]any)
			if payload["action"] == "ag_settings" && e.WorkspaceID == testWorkspaceID {
				mu.Lock()
				refreshes++
				mu.Unlock()
			}
		})
		agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": true, "backup_max_concurrent": 3})
		agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": true, "backup_max_concurrent": 3})
		mu.Lock()
		defer mu.Unlock()
		if refreshes != 1 {
			t.Fatalf("refreshes = %d, want 1", refreshes)
		}
	})

	t.Run("AG patch: the WebSocket ack carries the setting from the lease and a toggle arrives on the refresh", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": true, "backup_max_concurrent": 3})

		lease := daemonws.NewRuntimeLease(testWorkspaceID, "online", time.Now(), true)
		identity := daemonws.ClientIdentity{
			WorkspaceID:   testWorkspaceID,
			WorkspaceIDs:  []string{testWorkspaceID},
			RuntimeIDs:    []string{runtimeID},
			RuntimeLeases: map[string]*daemonws.RuntimeLease{runtimeID: lease},
		}
		beat := func() *protocol.DaemonHeartbeatAckPayload {
			ctx := agquota.WithReport(context.Background(), &protocol.DaemonQuotaReport{Account: "primary", EffectiveCap: 2, Max: 5})
			ack, err := testHandler.HandleDaemonWSHeartbeat(ctx, identity, runtimeID, true)
			if err != nil || ack == nil {
				t.Fatalf("ws heartbeat: %v (%v)", ack, err)
			}
			return ack
		}

		ack := beat()
		if ack.AGSettings == nil || !ack.AGSettings.BackupEnabled || ack.AGSettings.BackupMaxConcurrent != 3 {
			t.Fatalf("first ack setting = %+v", ack.AGSettings)
		}
		if memo := lease.AGQuota(); !memo.Known || memo.Settings == nil {
			t.Fatalf("lease memo = %+v", memo)
		}

		// A toggle does not reach the lease until its next refresh.
		agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": false, "backup_max_concurrent": 3})
		if ack = beat(); ack.AGSettings == nil || !ack.AGSettings.BackupEnabled {
			t.Fatalf("a beat inside the refresh interval re-read the row: %+v", ack.AGSettings)
		}
		memo := lease.AGQuota()
		memo.WrittenAt = memo.WrittenAt.Add(-agquota.RefreshInterval)
		lease.SetAGQuota(memo)
		if ack = beat(); ack.AGSettings == nil || ack.AGSettings.BackupEnabled {
			t.Fatalf("after the refresh interval ack setting = %+v, want disabled", ack.AGSettings)
		}
	})

	t.Run("AG patch: a WebSocket ack for a runtime with no stored setting carries none", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		lease := daemonws.NewRuntimeLease(testWorkspaceID, "online", time.Now(), true)
		identity := daemonws.ClientIdentity{
			WorkspaceID: testWorkspaceID, WorkspaceIDs: []string{testWorkspaceID}, RuntimeIDs: []string{runtimeID},
			RuntimeLeases: map[string]*daemonws.RuntimeLease{runtimeID: lease},
		}
		ctx := agquota.WithReport(context.Background(), &protocol.DaemonQuotaReport{Account: "primary", Max: 5})
		ack, err := testHandler.HandleDaemonWSHeartbeat(ctx, identity, runtimeID, true)
		if err != nil || ack == nil || ack.AGSettings != nil {
			t.Fatalf("ack = %+v (%v)", ack, err)
		}
	})

	t.Run("AG patch: registering again keeps ag_settings and clears the other metadata keys", func(t *testing.T) {
		daemonID := fmt.Sprintf("ag-settings-daemon-%d", time.Now().UnixNano())
		ws := parseUUID(testWorkspaceID)
		upsert := func(metadata string) db.UpsertAgentRuntimeRow {
			row, err := testHandler.Queries.UpsertAgentRuntime(context.Background(), db.UpsertAgentRuntimeParams{
				WorkspaceID: ws, DaemonID: pgtype.Text{String: daemonID, Valid: true}, Name: "ag settings", RuntimeMode: "local",
				Provider: "claude", Status: "online", DeviceInfo: "test", Metadata: []byte(metadata), OwnerID: parseUUID(testUserID),
			})
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}
			t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, row.ID) })
			return row
		}
		row := upsert(`{"cli_version":"v0.6.1"}`)
		runtimeID := uuidToString(row.ID)
		agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": true, "backup_max_concurrent": 3})
		agQuotaHeartbeat(t, runtimeID, map[string]any{
			"ag_quota": agQuotaReport(0.5), "ag_drain": map[string]any{"draining": true, "idle": false, "active_tasks": 1},
		})
		testPool.Exec(context.Background(), `UPDATE agent_runtime SET metadata = metadata || '{"offline_reason":"x"}'::jsonb WHERE id = $1`, runtimeID)

		upsert(`{"cli_version":"v0.6.2"}`)
		md := agDrainMetadata(t, runtimeID)
		if s := agquota.ParseSettings(md[agquota.SettingsKey]); s == nil || !s.BackupEnabled || s.BackupMaxConcurrent != 3 {
			t.Fatalf("ag_settings after re-register = %s", md[agquota.SettingsKey])
		}
		if string(md["cli_version"]) != `"v0.6.2"` {
			t.Fatalf("registration metadata not applied: %v", md)
		}
		for _, stale := range []string{"ag_quota", "ag_drain", "offline_reason"} {
			if _, present := md[stale]; present {
				t.Fatalf("%s survived registration: %v", stale, md)
			}
		}
	})

	t.Run("AG patch: registering a runtime with no stored setting adds no ag_settings key", func(t *testing.T) {
		daemonID := fmt.Sprintf("ag-settings-daemon-%d", time.Now().UnixNano())
		row, err := testHandler.Queries.UpsertAgentRuntime(context.Background(), db.UpsertAgentRuntimeParams{
			WorkspaceID: parseUUID(testWorkspaceID), DaemonID: pgtype.Text{String: daemonID, Valid: true}, Name: "ag settings", RuntimeMode: "local",
			Provider: "claude", Status: "online", DeviceInfo: "test", Metadata: []byte(`{}`), OwnerID: parseUUID(testUserID),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, row.ID) })
		if _, present := agDrainMetadata(t, uuidToString(row.ID))[agquota.SettingsKey]; present {
			t.Fatal("ag_settings present on a runtime that never had one")
		}
	})

	t.Run("AG patch: the custom-profile register path keeps ag_settings too", func(t *testing.T) {
		daemonID := fmt.Sprintf("ag-settings-daemon-%d", time.Now().UnixNano())
		profileID := parseUUID("00000000-0000-4000-8000-0000000a9501")
		upsert := func(metadata string) db.UpsertAgentRuntimeWithProfileRow {
			row, err := testHandler.Queries.UpsertAgentRuntimeWithProfile(context.Background(), db.UpsertAgentRuntimeWithProfileParams{
				WorkspaceID: parseUUID(testWorkspaceID), DaemonID: pgtype.Text{String: daemonID, Valid: true}, Name: "ag profile", RuntimeMode: "local",
				Provider: "claude", Status: "online", DeviceInfo: "test", Metadata: []byte(metadata), OwnerID: parseUUID(testUserID), ProfileID: profileID,
			})
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}
			t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, row.ID) })
			return row
		}
		row := upsert(`{}`)
		runtimeID := uuidToString(row.ID)
		agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": true, "backup_max_concurrent": 5})
		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.5)})

		upsert(`{"cli_version":"v0.6.2"}`)
		md := agDrainMetadata(t, runtimeID)
		if s := agquota.ParseSettings(md[agquota.SettingsKey]); s == nil || s.BackupMaxConcurrent != 5 {
			t.Fatalf("ag_settings after re-register = %s", md[agquota.SettingsKey])
		}
		if _, present := md[agquota.MetadataKey]; present {
			t.Fatalf("ag_quota survived registration: %v", md)
		}
	})
}

func TestAGPatchRuntimeSettingsValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("AG patch: a non-Claude runtime is rejected with 400", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET provider = 'codex' WHERE id = $1`, runtimeID); err != nil {
			t.Fatal(err)
		}
		if w := agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": true}); w.Code != http.StatusBadRequest {
			t.Fatalf("PUT on a Codex runtime = %d %s, want 400", w.Code, w.Body)
		}
		if _, present := agDrainMetadata(t, runtimeID)[agquota.SettingsKey]; present {
			t.Fatal("a setting was stored on a non-Claude runtime")
		}
	})

	t.Run("AG patch: out-of-range, missing and malformed bodies are rejected with 400", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		for name, body := range map[string]map[string]any{
			"zero limit":      {"backup_enabled": true, "backup_max_concurrent": 0},
			"negative limit":  {"backup_enabled": true, "backup_max_concurrent": -1},
			"limit too large": {"backup_enabled": true, "backup_max_concurrent": 101},
			"no enabled flag": {"backup_max_concurrent": 2},
			"string flag":     {"backup_enabled": "yes"},
		} {
			if w := agQuotaPutSettings(testUserID, runtimeID, body); w.Code != http.StatusBadRequest {
				t.Errorf("%s: PUT = %d %s, want 400", name, w.Code, w.Body)
			}
		}
		w := httptest.NewRecorder()
		req := newRequestAs(testUserID, http.MethodPut, "/api/runtimes/"+runtimeID+"/ag-settings", nil)
		req.Body = http.NoBody
		testHandler.UpdateAGRuntimeSettings(w, withURLParam(req, "runtimeId", runtimeID))
		if w.Code != http.StatusBadRequest {
			t.Errorf("empty body: PUT = %d, want 400", w.Code)
		}
		if _, present := agDrainMetadata(t, runtimeID)[agquota.SettingsKey]; present {
			t.Fatal("a rejected request stored a setting")
		}
	})

	t.Run("AG patch: the limit bounds 1 and 100 are accepted", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		for _, n := range []int{1, 100} {
			if w := agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": true, "backup_max_concurrent": n}); w.Code != http.StatusOK {
				t.Errorf("limit %d: PUT = %d %s", n, w.Code, w.Body)
			}
		}
	})

	t.Run("AG patch: only a runtime editor may PUT, and an unknown runtime is 404", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		other := createRuntimeLocalSkillTestMember(t, "member")
		if w := agQuotaPutSettings(other, runtimeID, map[string]any{"backup_enabled": true}); w.Code != http.StatusForbidden {
			t.Fatalf("plain member PUT = %d %s, want 403", w.Code, w.Body)
		}
		if w := agQuotaPutSettings(testUserID, "00000000-0000-4000-8000-000000000000", map[string]any{"backup_enabled": true}); w.Code != http.StatusNotFound {
			t.Fatalf("unknown runtime PUT = %d, want 404", w.Code)
		}
		if w := agQuotaPutSettings(testUserID, "not-a-uuid", map[string]any{"backup_enabled": true}); w.Code != http.StatusBadRequest {
			t.Fatalf("bad id PUT = %d, want 400", w.Code)
		}
	})
}
