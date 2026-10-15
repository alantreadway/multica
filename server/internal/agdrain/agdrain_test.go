package agdrain

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeStore models the guarded queries: a stored value, and rows affected 0
// when the write would not change it.
type fakeStore struct {
	stored  []byte // nil: no key
	sets    int
	clears  int
	fail    error
	lastRow pgtype.UUID
}

func (f *fakeStore) SetAGDrain(_ context.Context, id pgtype.UUID, state []byte) (int64, error) {
	f.sets++
	f.lastRow = id
	if f.fail != nil {
		return 0, f.fail
	}
	if string(f.stored) == string(state) {
		return 0, nil
	}
	f.stored = state
	return 1, nil
}

func (f *fakeStore) ClearAGDrain(_ context.Context, id pgtype.UUID) (int64, error) {
	f.clears++
	f.lastRow = id
	if f.fail != nil {
		return 0, f.fail
	}
	if f.stored == nil {
		return 0, nil
	}
	f.stored = nil
	return 1, nil
}

var (
	t0      = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	runtime = pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
)

func draining(active int64, idle bool) *protocol.DaemonDrainReport {
	return &protocol.DaemonDrainReport{Draining: true, Idle: idle, ActiveTasks: active}
}

func TestAGPatchDrainStoreOnChange(t *testing.T) {
	t.Run("AG patch: a state change writes, the same state again does not", func(t *testing.T) {
		store := &fakeStore{}
		memo := MemoFromMetadata([]byte(`{"cli_version":"v0.6.2"}`), t0)

		memo, changed, err := Apply(context.Background(), store, runtime, memo, draining(2, false), t0)
		if err != nil || !changed || store.sets != 1 {
			t.Fatalf("first report: changed=%v sets=%d err=%v, want one write", changed, store.sets, err)
		}
		var got State
		if err := json.Unmarshal(store.stored, &got); err != nil || got != (State{Draining: true, ActiveTasks: 2}) {
			t.Fatalf("stored = %s (%v), want draining with 2 active", store.stored, err)
		}

		for i := 0; i < 5; i++ {
			memo, changed, err = Apply(context.Background(), store, runtime, memo, draining(2, false), t0.Add(time.Duration(i)*time.Second))
			if err != nil || changed {
				t.Fatalf("repeat %d: changed=%v err=%v", i, changed, err)
			}
		}
		if store.sets != 1 || store.clears != 0 {
			t.Fatalf("sets=%d clears=%d after repeats, want 1 and 0", store.sets, store.clears)
		}

		_, changed, err = Apply(context.Background(), store, runtime, memo, draining(0, true), t0.Add(10*time.Second))
		if err != nil || !changed || store.sets != 2 {
			t.Fatalf("idle report: changed=%v sets=%d err=%v, want a second write", changed, store.sets, err)
		}
	})

	t.Run("AG patch: not draining clears a stored report once, then costs nothing", func(t *testing.T) {
		store := &fakeStore{stored: []byte(`{"draining":true}`)}
		memo := MemoFromMetadata([]byte(`{"ag_drain":{"draining":true,"idle":false,"active_tasks":1}}`), t0)
		off := &protocol.DaemonDrainReport{}

		memo, changed, err := Apply(context.Background(), store, runtime, memo, off, t0)
		if err != nil || !changed || store.clears != 1 || store.stored != nil {
			t.Fatalf("clear: changed=%v clears=%d stored=%s err=%v", changed, store.clears, store.stored, err)
		}
		for i := 0; i < 5; i++ {
			memo, changed, err = Apply(context.Background(), store, runtime, memo, off, t0.Add(time.Duration(i+1)*time.Minute))
			if err != nil || changed {
				t.Fatalf("repeat %d: changed=%v err=%v", i, changed, err)
			}
		}
		if store.clears != 1 {
			t.Fatalf("clears = %d, want 1: not draining must not query per heartbeat", store.clears)
		}
	})

	t.Run("AG patch: a heartbeat without the report leaves metadata alone", func(t *testing.T) {
		store := &fakeStore{stored: []byte(`{"draining":true}`)}
		memo := MemoFromMetadata([]byte(`{"ag_drain":{"draining":true,"idle":false,"active_tasks":1}}`), t0)

		next, changed, err := Apply(context.Background(), store, runtime, memo, nil, t0)
		if err != nil || changed || store.sets != 0 || store.clears != 0 {
			t.Fatalf("nil report: changed=%v sets=%d clears=%d err=%v", changed, store.sets, store.clears, err)
		}
		if next != memo {
			t.Fatalf("memo changed on a nil report: %+v -> %+v", memo, next)
		}
	})

	t.Run("AG patch: an unknown state is reconciled once with the guarded query", func(t *testing.T) {
		// A fresh WebSocket lease has not seen the stored state.
		store := &fakeStore{}
		off := &protocol.DaemonDrainReport{}

		memo, changed, err := Apply(context.Background(), store, runtime, Memo{}, off, t0)
		if err != nil || changed || store.clears != 1 {
			t.Fatalf("first: changed=%v clears=%d err=%v, want one guarded no-op", changed, store.clears, err)
		}
		if _, _, err := Apply(context.Background(), store, runtime, memo, off, t0.Add(time.Second)); err != nil || store.clears != 1 {
			t.Fatalf("second: clears=%d err=%v, want no further query", store.clears, err)
		}
	})

	t.Run("AG patch: a draining lease re-checks after the refresh interval", func(t *testing.T) {
		// Registration overwrites metadata, so the key can vanish under a
		// connection that still remembers it.
		store := &fakeStore{}
		report := draining(1, false)

		memo, _, err := Apply(context.Background(), store, runtime, Memo{}, report, t0)
		if err != nil || store.sets != 1 {
			t.Fatalf("seed: sets=%d err=%v", store.sets, err)
		}
		store.stored = nil // re-registered: key gone

		memo, changed, err := Apply(context.Background(), store, runtime, memo, report, t0.Add(RefreshInterval-time.Second))
		if err != nil || changed || store.sets != 1 {
			t.Fatalf("before interval: changed=%v sets=%d err=%v", changed, store.sets, err)
		}
		_, changed, err = Apply(context.Background(), store, runtime, memo, report, t0.Add(RefreshInterval))
		if err != nil || !changed || store.sets != 2 {
			t.Fatalf("at interval: changed=%v sets=%d err=%v, want the key restored", changed, store.sets, err)
		}
	})

	t.Run("AG patch: a failed write is retried on the next heartbeat", func(t *testing.T) {
		store := &fakeStore{fail: errors.New("db down")}
		report := draining(1, false)

		memo, changed, err := Apply(context.Background(), store, runtime, MemoFromMetadata(nil, t0), report, t0)
		if err == nil || changed {
			t.Fatalf("changed=%v err=%v, want the error surfaced", changed, err)
		}
		store.fail = nil
		_, changed, err = Apply(context.Background(), store, runtime, memo, report, t0.Add(time.Second))
		if err != nil || !changed || store.sets != 2 {
			t.Fatalf("retry: changed=%v sets=%d err=%v", changed, store.sets, err)
		}
	})
}

func TestAGPatchDrainReportNormalised(t *testing.T) {
	t.Run("AG patch: a negative count is stored as zero and idle needs zero running", func(t *testing.T) {
		store := &fakeStore{}
		if _, _, err := Apply(context.Background(), store, runtime, Memo{}, draining(-4, true), t0); err != nil {
			t.Fatal(err)
		}
		var got State
		_ = json.Unmarshal(store.stored, &got)
		if got != (State{Draining: true, Idle: true, ActiveTasks: 0}) {
			t.Fatalf("stored = %+v", got)
		}

		store = &fakeStore{}
		if _, _, err := Apply(context.Background(), store, runtime, Memo{}, draining(3, true), t0); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(store.stored, &got)
		if got.Idle {
			t.Fatalf("idle with 3 running: %+v", got)
		}
	})

	t.Run("AG patch: unreadable or non-draining stored metadata yields no drain state", func(t *testing.T) {
		for name, md := range map[string][]byte{
			"empty":         nil,
			"not json":      []byte(`{`),
			"wrong shape":   []byte(`{"ag_drain":"yes"}`),
			"not draining":  []byte(`{"ag_drain":{"draining":false}}`),
			"other keys":    []byte(`{"offline_reason":{"code":"x"}}`),
			"null":          []byte(`{"ag_drain":null}`),
			"array":         []byte(`[1]`),
			"nested number": []byte(`{"ag_drain":{"draining":1}}`),
		} {
			m := MemoFromMetadata(md, t0)
			if m.State != nil {
				t.Errorf("%s: State = %+v, want nil", name, m.State)
			}
		}
	})
}

func TestAGPatchDrainReportContext(t *testing.T) {
	t.Run("AG patch: the WebSocket handler gets the report through the context", func(t *testing.T) {
		if ReportFrom(context.Background()) != nil {
			t.Fatal("report on a bare context")
		}
		if got := ReportFrom(WithReport(context.Background(), nil)); got != nil {
			t.Fatalf("nil report round-tripped as %+v", got)
		}
		r := draining(1, false)
		if got := ReportFrom(WithReport(context.Background(), r)); got != r {
			t.Fatalf("got %+v, want %+v", got, r)
		}
	})
}
