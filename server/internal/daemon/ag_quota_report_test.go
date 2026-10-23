package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agReportServer answers every heartbeat with ack, recording the last body.
type agReportServer struct {
	*httptest.Server
	mu   sync.Mutex
	body map[string]any
	ack  string
}

func newAGReportServer(t *testing.T, ack string) *agReportServer {
	t.Helper()
	s := &agReportServer{ack: ack}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.body = body
		ack := s.ack
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(ack))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *agReportServer) lastBody() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body
}

func (s *agReportServer) setAck(ack string) {
	s.mu.Lock()
	s.ack = ack
	s.mu.Unlock()
}

func TestAGPatchQuotaReportContent(t *testing.T) {
	t.Run("AG patch: the report carries the two paced windows rounded, the cap, max and the default backup block", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		f.d.agQuota.now = func() time.Time { return now.Add(30 * time.Second) }
		reset5, reset7 := now.Add(3*time.Hour), now.Add(100*time.Hour)
		f.event(t, 0.8449, 0.6250, reset5, reset7)

		r := f.d.agQuotaReport("rt-claude")
		if r == nil {
			t.Fatal("no report for a Claude runtime")
		}
		if r.Account != "primary" || r.Max != 5 || r.Hold != nil {
			t.Fatalf("account/max/hold = %q/%d/%v", r.Account, r.Max, r.Hold)
		}
		five, seven := r.Windows["five_hour"], r.Windows["seven_day"]
		if five.Utilization == nil || *five.Utilization != 0.84 || seven.Utilization == nil || *seven.Utilization != 0.63 {
			t.Fatalf("utilisation = %v / %v, want 0.84 and 0.63", five.Utilization, seven.Utilization)
		}
		if five.ResetsAt != reset5.UTC().Format(time.RFC3339) || seven.ResetsAt != reset7.UTC().Format(time.RFC3339) {
			t.Fatalf("resets_at = %q / %q", five.ResetsAt, seven.ResetsAt)
		}
		if want := now.Truncate(time.Minute).Format(time.RFC3339); five.ObservedAt != want {
			t.Fatalf("observed_at = %q, want %q (the minute)", five.ObservedAt, want)
		}
		if r.EffectiveCap != f.d.agPaceHealth().EffectiveCap {
			t.Fatalf("effective_cap = %d, want the pace limiter's %d", r.EffectiveCap, f.d.agPaceHealth().EffectiveCap)
		}
		if r.Backup.Detected || r.Backup.Valid || r.Backup.Reason != "" || r.Backup.Enabled || r.Backup.MaxConcurrent != 2 {
			t.Fatalf("backup = %+v, want undetected, disabled, 2", r.Backup)
		}
		if r.BackupWindows == nil || len(r.BackupWindows) != 0 {
			t.Fatalf("backup_windows = %v, want an empty object", r.BackupWindows)
		}
	})

	t.Run("AG patch: the report is stable between heartbeats so the server's guarded write is a no-op", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		f.event(t, 0.50, 0.40, now.Add(3*time.Hour), now.Add(100*time.Hour))
		a, _ := json.Marshal(f.d.agQuotaReport("rt-claude"))
		f.event(t, 0.5004, 0.4001, now.Add(3*time.Hour), now.Add(100*time.Hour)) // rounds to the same
		b, _ := json.Marshal(f.d.agQuotaReport("rt-claude"))
		if string(a) != string(b) {
			t.Fatalf("report changed without a real change:\n%s\n%s", a, b)
		}
	})

	t.Run("AG patch: a hold reports effective_cap 0 with its end and window, and clears with the hold", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		now := f.clock.now()
		reset5 := now.Add(2 * time.Hour)
		raw := fmt.Sprintf(`{"status":"rejected","rateLimitType":"five_hour","resetsAt":%d,"unifiedWindows":{"five_hour":{"utilization":1,"resetsAt":%d}}}`, reset5.Unix(), reset5.Unix())
		agQuotaObs(f.d, Task{ID: "t1"}, "claude", nil)(json.RawMessage(raw))

		r := f.d.agQuotaReport("rt-claude")
		if r.EffectiveCap != 0 || r.Hold == nil || r.Hold.Window != "five_hour" {
			t.Fatalf("held report = cap %d hold %+v, want cap 0 and a five_hour hold", r.EffectiveCap, r.Hold)
		}
		if want := reset5.Add(agQuotaResetGrace).UTC().Format(time.RFC3339); r.Hold.Until != want {
			t.Fatalf("hold.until = %q, want %q", r.Hold.Until, want)
		}

		f.clock.set(reset5.Add(agQuotaResetGrace + time.Minute))
		r = f.d.agQuotaReport("rt-claude")
		if r.Hold != nil || r.EffectiveCap == 0 {
			t.Fatalf("after the hold: cap %d hold %+v", r.EffectiveCap, r.Hold)
		}
	})

	t.Run("AG patch: a daemon that has seen no quota reports empty windows, not null", func(t *testing.T) {
		f := newAGPaceFixture(t, 3)
		raw, _ := json.Marshal(f.d.agQuotaReport("rt-claude"))
		var got map[string]json.RawMessage
		_ = json.Unmarshal(raw, &got)
		if string(got["windows"]) != "{}" || string(got["backup_windows"]) != "{}" {
			t.Fatalf("report = %s", raw)
		}
	})
}

func TestAGPatchQuotaReportOnlyOnClaudeRuntimes(t *testing.T) {
	t.Run("AG patch: a Codex runtime and an unknown runtime get no report", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		for _, id := range []string{"rt-codex", "rt-unknown"} {
			if r := f.d.agQuotaReport(id); r != nil {
				t.Errorf("report for %s = %+v, want nil", id, r)
			}
		}
		if f.d.agQuotaReport("rt-claude") == nil || f.d.agQuotaReport("rt-claude-b") == nil {
			t.Error("Claude runtimes (built-in and custom profile) must report")
		}
	})

	t.Run("AG patch: the HTTP heartbeat carries ag_quota for Claude runtimes only", func(t *testing.T) {
		srv := newAGReportServer(t, `{"status":"ok"}`)
		f := newAGPaceFixtureAt(t, 5, srv.URL)

		f.d.runHeartbeatTick(context.Background(), "rt-claude")
		if _, ok := srv.lastBody()["ag_quota"].(map[string]any); !ok {
			t.Fatalf("Claude heartbeat body = %v, want ag_quota", srv.lastBody())
		}
		f.d.runHeartbeatTick(context.Background(), "rt-codex")
		if body := srv.lastBody(); body == nil || body["runtime_id"] != "rt-codex" {
			t.Fatalf("no heartbeat for rt-codex reached the server: %v", body)
		} else if _, present := body["ag_quota"]; present {
			t.Fatalf("Codex heartbeat carries ag_quota: %v", body)
		}
	})

	t.Run("AG patch: the WebSocket heartbeat frames carry ag_quota for Claude runtimes only", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		writes := make(chan *wsOutbound, 4)
		f.d.sendWSHeartbeats(context.Background(), []string{"rt-claude", "rt-codex"}, writes)

		for _, want := range []struct {
			id    string
			quota bool
		}{{"rt-claude", true}, {"rt-codex", false}} {
			out := <-writes
			var msg protocol.Message
			var payload protocol.DaemonHeartbeatRequestPayload
			if err := json.Unmarshal(out.data, &msg); err != nil || json.Unmarshal(msg.Payload, &payload) != nil {
				t.Fatalf("decode frame: %v", err)
			}
			if payload.RuntimeID != want.id || (payload.AGQuota != nil) != want.quota {
				t.Fatalf("%s frame: ag_quota present = %v, want %v", payload.RuntimeID, payload.AGQuota != nil, want.quota)
			}
			if payload.AGDrain == nil {
				t.Fatalf("%s frame lost ag_drain", want.id)
			}
		}
	})

	t.Run("AG patch: the plain and drain-only heartbeat calls omit ag_quota", func(t *testing.T) {
		srv := newAGReportServer(t, `{"status":"ok"}`)
		f := newAGPaceFixtureAt(t, 5, srv.URL)
		if _, err := f.d.client.SendHeartbeat(context.Background(), "rt-claude"); err != nil {
			t.Fatal(err)
		}
		if _, present := srv.lastBody()["ag_quota"]; present {
			t.Fatalf("plain heartbeat carries ag_quota: %v", srv.lastBody())
		}
		if _, err := f.d.client.SendHeartbeatWithDrain(context.Background(), "rt-claude", f.d.agDrainReport()); err != nil {
			t.Fatal(err)
		}
		if _, present := srv.lastBody()["ag_quota"]; present {
			t.Fatalf("drain-only heartbeat carries ag_quota: %v", srv.lastBody())
		}
	})
}

func TestAGPatchBackupSettingFromAck(t *testing.T) {
	const ackOn = `{"status":"ok","ag_settings":{"backup_enabled":true,"backup_max_concurrent":3}}`

	t.Run("AG patch: with no setting ever received, backup is disabled", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		if s := f.d.agBackupSetting("rt-claude"); s.BackupEnabled || s.BackupMaxConcurrent != 2 {
			t.Fatalf("setting = %+v, want disabled with the default 2", s)
		}
	})

	t.Run("AG patch: an HTTP heartbeat ack's setting is applied and shows in the next report", func(t *testing.T) {
		srv := newAGReportServer(t, ackOn)
		f := newAGPaceFixtureAt(t, 5, srv.URL)

		f.d.runHeartbeatTick(context.Background(), "rt-claude")
		if s := f.d.agBackupSetting("rt-claude"); !s.BackupEnabled || s.BackupMaxConcurrent != 3 {
			t.Fatalf("setting = %+v", s)
		}
		if b := f.d.agQuotaReport("rt-claude").Backup; !b.Enabled || b.MaxConcurrent != 3 {
			t.Fatalf("report backup = %+v, want enabled with 3", b)
		}
		if s := f.d.agBackupSetting("rt-claude-b"); s.BackupEnabled {
			t.Fatalf("another runtime picked the setting up: %+v", s)
		}
	})

	t.Run("AG patch: a WebSocket heartbeat ack's setting is applied", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		var ack HeartbeatResponse
		if err := json.Unmarshal([]byte(`{"runtime_id":"rt-claude","status":"ok","ag_settings":{"backup_enabled":true,"backup_max_concurrent":4}}`), &ack); err != nil {
			t.Fatal(err)
		}
		f.d.handleWSHeartbeatAck(context.Background(), &ack)
		if s := f.d.agBackupSetting("rt-claude"); !s.BackupEnabled || s.BackupMaxConcurrent != 4 {
			t.Fatalf("setting = %+v", s)
		}
	})

	t.Run("AG patch: a missed ack keeps the last setting for ten minutes, then backup fails closed", func(t *testing.T) {
		srv := newAGReportServer(t, ackOn)
		f := newAGPaceFixtureAt(t, 5, srv.URL)
		f.d.runHeartbeatTick(context.Background(), "rt-claude")

		srv.setAck(`{"status":"ok"}`) // an ack without the setting
		start := f.clock.now()
		f.clock.set(start.Add(agSettingsTTL - time.Second))
		f.d.runHeartbeatTick(context.Background(), "rt-claude")
		if s := f.d.agBackupSetting("rt-claude"); !s.BackupEnabled {
			t.Fatalf("setting dropped inside the TTL: %+v", s)
		}
		f.clock.set(start.Add(agSettingsTTL))
		if s := f.d.agBackupSetting("rt-claude"); s.BackupEnabled || s.BackupMaxConcurrent != 2 {
			t.Fatalf("setting after the TTL = %+v, want disabled", s)
		}
	})

	t.Run("AG patch: a fresh ack renews the TTL", func(t *testing.T) {
		srv := newAGReportServer(t, ackOn)
		f := newAGPaceFixtureAt(t, 5, srv.URL)
		start := f.clock.now()
		f.d.runHeartbeatTick(context.Background(), "rt-claude")
		f.clock.set(start.Add(agSettingsTTL - time.Minute))
		f.d.runHeartbeatTick(context.Background(), "rt-claude")
		f.clock.set(start.Add(agSettingsTTL + time.Minute))
		if s := f.d.agBackupSetting("rt-claude"); !s.BackupEnabled {
			t.Fatalf("setting expired despite a later ack: %+v", s)
		}
	})

	t.Run("AG patch: a later ack changes or disables the setting at once", func(t *testing.T) {
		srv := newAGReportServer(t, ackOn)
		f := newAGPaceFixtureAt(t, 5, srv.URL)
		f.d.runHeartbeatTick(context.Background(), "rt-claude")
		srv.setAck(`{"status":"ok","ag_settings":{"backup_enabled":false,"backup_max_concurrent":3}}`)
		f.d.runHeartbeatTick(context.Background(), "rt-claude")
		if s := f.d.agBackupSetting("rt-claude"); s.BackupEnabled {
			t.Fatalf("setting = %+v, want disabled", s)
		}
	})

	t.Run("AG patch: an unusable setting is ignored rather than applied", func(t *testing.T) {
		f := newAGPaceFixture(t, 5)
		f.d.agApplySettings("rt-claude", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 0})
		f.d.agApplySettings("", &protocol.AGRuntimeSettings{BackupEnabled: true, BackupMaxConcurrent: 2})
		f.d.agApplySettings("rt-claude", nil)
		if s := f.d.agBackupSetting("rt-claude"); s.BackupEnabled {
			t.Fatalf("setting = %+v, want disabled", s)
		}
	})
}
