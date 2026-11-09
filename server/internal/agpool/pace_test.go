package agpool

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestAGPatchRuntimePoolPace(t *testing.T) {
	const reset = "2026-11-14T06:00:00Z"
	self := PaceRuntime{ID: "b", Self: true, Slots: 2, WeeklyResetsAt: reset}
	cases := []struct {
		name          string
		rows          []PaceRuntime
		reportReset   string
		reportMax     int
		want          protocol.AGPoolPace
		wantNotPooled bool
	}{
		{name: "no self row: not pooled", rows: nil, wantNotPooled: true},
		{name: "alone in the pool", rows: []PaceRuntime{self}, reportReset: reset, reportMax: 2, want: protocol.AGPoolPace{Slots: 2, Lead: true}},
		{
			name:        "peers on the same account add up",
			rows:        []PaceRuntime{self, {ID: "c", Active: 2, Slots: 3, WeeklyResetsAt: reset}, {ID: "d", Active: 1, Slots: 1, WeeklyResetsAt: "2026-11-14T06:01:00Z"}},
			reportReset: reset, reportMax: 2,
			want: protocol.AGPoolPace{PeerActive: 3, PeerSlots: 4, Slots: 2},
		},
		{
			name:        "a peer whose weekly reset differs is another account",
			rows:        []PaceRuntime{self, {ID: "c", Active: 2, Slots: 3, WeeklyResetsAt: "2026-11-12T18:00:00Z"}},
			reportReset: reset, reportMax: 2,
			want: protocol.AGPoolPace{Slots: 2, Lead: true},
		},
		{
			name:        "a peer with no reported reset is assumed to share the account",
			rows:        []PaceRuntime{self, {ID: "c", Active: 1, Slots: 3}},
			reportReset: reset, reportMax: 2,
			want: protocol.AGPoolPace{PeerActive: 1, PeerSlots: 3, Slots: 2},
		},
		{
			name:        "the heartbeat's report wins over the stored self reset",
			rows:        []PaceRuntime{{ID: "b", Self: true, Slots: 2, WeeklyResetsAt: "2026-11-07T06:00:00Z"}, {ID: "c", Active: 1, Slots: 3, WeeklyResetsAt: reset}},
			reportReset: reset, reportMax: 2,
			want: protocol.AGPoolPace{PeerActive: 1, PeerSlots: 3, Slots: 2},
		},
		{
			name:      "the stored self reset is used when the report has none",
			rows:      []PaceRuntime{self, {ID: "c", Active: 1, Slots: 3, WeeklyResetsAt: "2026-11-12T18:00:00Z"}},
			reportMax: 2,
			want:      protocol.AGPoolPace{Slots: 2, Lead: true},
		},
		{
			name:        "unknown slots: self falls back to the report's max, a peer to self's slots",
			rows:        []PaceRuntime{{ID: "b", Self: true, Slots: -1}, {ID: "c", Active: 1, Slots: -1}},
			reportReset: reset, reportMax: 3,
			want: protocol.AGPoolPace{PeerActive: 1, PeerSlots: 3, Slots: 3, Lead: true},
		},
		{
			name:        "the lead is the most slots, then the lowest ID",
			rows:        []PaceRuntime{self, {ID: "a", Slots: 2, WeeklyResetsAt: reset}, {ID: "c", Slots: 2, WeeklyResetsAt: reset}},
			reportReset: reset, reportMax: 2,
			want: protocol.AGPoolPace{PeerSlots: 4, Slots: 2},
		},
		{
			name:        "a draining peer (0 slots) does not take the lead",
			rows:        []PaceRuntime{self, {ID: "a", Active: 2, Slots: 0, WeeklyResetsAt: reset}},
			reportReset: reset, reportMax: 2,
			want: protocol.AGPoolPace{PeerActive: 2, Slots: 2, Lead: true},
		},
	}
	for _, c := range cases {
		t.Run("AG patch: "+c.name, func(t *testing.T) {
			got, ok := Pace(c.rows, c.reportReset, c.reportMax)
			if ok == c.wantNotPooled {
				t.Fatalf("ok = %v", ok)
			}
			if got != c.want {
				t.Fatalf("Pace = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestAGPatchRuntimePoolInMetadata(t *testing.T) {
	for in, want := range map[string]bool{
		``:                    false,
		`{}`:                  false,
		`{"ag_pool":""}`:      false,
		`{"ag_pool":"alan"}`:  true,
		`not json`:            false,
		`{"ag_pool":{"x":1}}`: false,
	} {
		if got := InMetadata([]byte(in)); got != want {
			t.Errorf("AG patch: InMetadata(%q) = %v, want %v", in, got, want)
		}
	}
}
