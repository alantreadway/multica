package handler

// These need the test database and are not run by ag-patches/test.sh; see its header.
// Spec-derived cases that sit beside ag_quota_test.go and reuse its helpers.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/agquota"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func agQuotaPutRaw(actorID, runtimeID, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := newRequestAs(actorID, http.MethodPut, "/api/runtimes/"+runtimeID+"/ag-settings", nil)
	req.Body = http.NoBody
	if body != "" {
		req.Body = io.NopCloser(strings.NewReader(body))
	}
	req.Header.Set("Content-Type", "application/json")
	testHandler.UpdateAGRuntimeSettings(w, withURLParam(req, "runtimeId", runtimeID))
	return w
}

func TestAGPatchRuntimeSettingsSpecCases(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("AG patch: a null flag or a fractional or non-numeric limit is rejected with 400 and stores nothing", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		for name, body := range map[string]string{
			"null flag":        `{"backup_enabled":null}`,
			"fractional limit": `{"backup_enabled":true,"backup_max_concurrent":2.5}`,
			"string limit":     `{"backup_enabled":true,"backup_max_concurrent":"3"}`,
			"not an object":    `[true]`,
		} {
			if w := agQuotaPutRaw(testUserID, runtimeID, body); w.Code != http.StatusBadRequest {
				t.Errorf("%s: PUT = %d %s, want 400", name, w.Code, w.Body)
			}
		}
		if _, present := agDrainMetadata(t, runtimeID)[agquota.SettingsKey]; present {
			t.Fatal("a rejected request stored a setting")
		}
	})

	t.Run("AG patch: a null limit takes the default of 2", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		w := agQuotaPutRaw(testUserID, runtimeID, `{"backup_enabled":true,"backup_max_concurrent":null}`)
		var got protocol.AGRuntimeSettings
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || !got.BackupEnabled || got.BackupMaxConcurrent != 2 {
			t.Fatalf("PUT = %d %s, want enabled with 2", w.Code, w.Body)
		}
	})

	t.Run("AG patch: a workspace admin who does not own the runtime may change it, and a non-member gets 404", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		admin := createRuntimeLocalSkillTestMember(t, "admin")
		if w := agQuotaPutSettings(admin, runtimeID, map[string]any{"backup_enabled": true, "backup_max_concurrent": 3}); w.Code != http.StatusOK {
			t.Fatalf("admin PUT = %d %s, want 200", w.Code, w.Body)
		}

		var outsider string
		if err := testPool.QueryRow(context.Background(),
			`INSERT INTO "user" (name, email) VALUES ('Outsider', $1) RETURNING id`,
			"ag-settings-outsider-"+time.Now().Format("150405.000000000")+"@multica.ai").Scan(&outsider); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, outsider) })
		if w := agQuotaPutSettings(outsider, runtimeID, map[string]any{"backup_enabled": false, "backup_max_concurrent": 5}); w.Code != http.StatusNotFound {
			t.Fatalf("non-member PUT = %d %s, want 404", w.Code, w.Body)
		}
		s := agquota.ParseSettings(agDrainMetadata(t, runtimeID)[agquota.SettingsKey])
		if s == nil || !s.BackupEnabled || s.BackupMaxConcurrent != 3 {
			t.Fatalf("the outsider's request changed the setting: %+v", s)
		}
	})

	t.Run("AG patch: a second PUT reaches the very next HTTP ack, and another runtime's ack is untouched", func(t *testing.T) {
		a := createRuntimeLocalSkillTestRuntime(t, testUserID)
		b := createRuntimeLocalSkillTestRuntime(t, testUserID)
		agQuotaPutSettings(testUserID, a, map[string]any{"backup_enabled": true, "backup_max_concurrent": 3})
		agQuotaPutSettings(testUserID, a, map[string]any{"backup_enabled": false, "backup_max_concurrent": 6})

		got, _ := agQuotaHeartbeat(t, a, map[string]any{})["ag_settings"].(map[string]any)
		if got["backup_enabled"] != false || got["backup_max_concurrent"] != float64(6) {
			t.Fatalf("ack after the second PUT = %v", got)
		}
		if other := agQuotaHeartbeat(t, b, map[string]any{})["ag_settings"]; other != nil {
			t.Fatalf("another runtime's ack carries %v", other)
		}
	})

	t.Run("AG patch: quota reports and PUTs leave each other's metadata alone", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": true, "backup_max_concurrent": 3})
		agQuotaHeartbeat(t, runtimeID, map[string]any{
			"ag_quota": agQuotaReport(0.5),
			"ag_drain": map[string]any{"draining": true, "idle": false, "active_tasks": 1},
		})
		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.7)})
		md := agDrainMetadata(t, runtimeID)
		if s := agquota.ParseSettings(md[agquota.SettingsKey]); s == nil || !s.BackupEnabled || s.BackupMaxConcurrent != 3 {
			t.Fatalf("report writes disturbed ag_settings: %s", md[agquota.SettingsKey])
		}

		agQuotaPutSettings(testUserID, runtimeID, map[string]any{"backup_enabled": false, "backup_max_concurrent": 4})
		md = agDrainMetadata(t, runtimeID)
		for _, key := range []string{agquota.MetadataKey, "ag_drain"} {
			if _, ok := md[key]; !ok {
				t.Fatalf("a settings PUT dropped %s: %v", key, md)
			}
		}
	})

	t.Run("AG patch: an unusable stored setting is not sent on the HTTP or WebSocket ack", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		if _, err := testPool.Exec(context.Background(),
			`UPDATE agent_runtime SET metadata = metadata || '{"ag_settings":{"backup_enabled":true,"backup_max_concurrent":0}}'::jsonb WHERE id = $1`,
			runtimeID); err != nil {
			t.Fatal(err)
		}
		if got := agQuotaHeartbeat(t, runtimeID, map[string]any{})["ag_settings"]; got != nil {
			t.Fatalf("HTTP ack carries %v from an out-of-range row", got)
		}

		lease := daemonws.NewRuntimeLease(testWorkspaceID, "online", time.Now(), true)
		identity := daemonws.ClientIdentity{
			WorkspaceID: testWorkspaceID, WorkspaceIDs: []string{testWorkspaceID}, RuntimeIDs: []string{runtimeID},
			RuntimeLeases: map[string]*daemonws.RuntimeLease{runtimeID: lease},
		}
		ctx := agquota.WithReport(context.Background(), &protocol.DaemonQuotaReport{Account: "primary", Max: 5})
		ack, err := testHandler.HandleDaemonWSHeartbeat(ctx, identity, runtimeID, true)
		if err != nil || ack == nil || ack.AGSettings != nil {
			t.Fatalf("WS ack = %+v (%v), want no setting", ack, err)
		}
	})

	t.Run("AG patch: the stored report keeps the documented shape", func(t *testing.T) {
		runtimeID := createRuntimeLocalSkillTestRuntime(t, testUserID)
		agQuotaHeartbeat(t, runtimeID, map[string]any{"ag_quota": agQuotaReport(0.84)})
		var stored map[string]any
		if err := json.Unmarshal(agDrainMetadata(t, runtimeID)[agquota.MetadataKey], &stored); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"account", "effective_cap", "max", "windows", "backup", "backup_windows"} {
			if _, ok := stored[key]; !ok {
				t.Errorf("ag_quota lacks %q: %v", key, stored)
			}
		}
		five, _ := stored["windows"].(map[string]any)["five_hour"].(map[string]any)
		if five["utilization"] != 0.84 || five["resets_at"] == nil || five["observed_at"] == nil {
			t.Errorf("five_hour = %v", five)
		}
	})
}
