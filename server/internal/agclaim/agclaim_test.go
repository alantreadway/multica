package agclaim

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

const (
	rtA = "11111111-1111-4111-8111-111111111111"
	rtB = "22222222-2222-4222-8222-222222222222"
)

func TestAGPatchClaimFilterParse(t *testing.T) {
	t.Run("AG patch: a minimum priority allows itself and everything above", func(t *testing.T) {
		f, err := Parse(map[string]string{rtA: "urgent", rtB: "medium"})
		if err != nil {
			t.Fatal(err)
		}
		want := Filter{
			rtA: {"urgent"},
			rtB: {"urgent", "high", "medium"},
		}
		if !reflect.DeepEqual(f, want) {
			t.Fatalf("filter = %v, want %v", f, want)
		}
	})

	t.Run("AG patch: none allows every priority", func(t *testing.T) {
		f, err := Parse(map[string]string{rtA: "none"})
		if err != nil || len(f[rtA]) != 5 {
			t.Fatalf("filter = %v (%v)", f, err)
		}
	})

	t.Run("AG patch: runtime ids are canonicalised", func(t *testing.T) {
		f, err := Parse(map[string]string{strings.ToUpper(rtA): "high"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f[rtA]; !ok {
			t.Fatalf("filter keys = %v, want the lowercase uuid", f)
		}
	})

	t.Run("AG patch: an absent or empty map is no filter", func(t *testing.T) {
		for _, in := range []map[string]string{nil, {}} {
			f, err := Parse(in)
			if err != nil || f != nil {
				t.Fatalf("Parse(%v) = %v, %v; want nil, nil", in, f, err)
			}
		}
	})

	t.Run("AG patch: an unknown priority or runtime id fails closed", func(t *testing.T) {
		for _, in := range []map[string]string{
			{rtA: "critical"},
			{rtA: ""},
			{rtA: "URGENT"},
			{"not-a-uuid": "urgent"},
			{rtA: "urgent", rtB: "bogus"},
		} {
			if f, err := Parse(in); err == nil || f != nil {
				t.Errorf("Parse(%v) = %v, %v; want an error", in, f, err)
			}
		}
	})
}

func TestAGPatchClaimFilterJSON(t *testing.T) {
	t.Run("AG patch: the query parameter is an object of runtime id to allowed priorities", func(t *testing.T) {
		f, _ := Parse(map[string]string{rtA: "high"})
		var got map[string][]string
		if err := json.Unmarshal(f.JSON(), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, map[string][]string{rtA: {"urgent", "high"}}) {
			t.Fatalf("json = %v", got)
		}
	})

	t.Run("AG patch: no filter encodes as SQL NULL", func(t *testing.T) {
		if b := (Filter)(nil).JSON(); b != nil {
			t.Fatalf("nil filter json = %s", b)
		}
		if b := (Filter{}).JSON(); b != nil {
			t.Fatalf("empty filter json = %s", b)
		}
	})
}

func TestAGPatchClaimFilterContext(t *testing.T) {
	f, _ := Parse(map[string]string{rtA: "urgent"})
	ctx := With(context.Background(), f)
	a, b := util.MustParseUUID(rtA), util.MustParseUUID(rtB)

	t.Run("AG patch: the filter travels in the context and answers per runtime", func(t *testing.T) {
		if !From(ctx).Filtered(a) {
			t.Fatal("runtime A should be filtered")
		}
		if From(ctx).Filtered(b) {
			t.Fatal("runtime B should not be filtered")
		}
	})

	t.Run("AG patch: a single-runtime query gets only that runtime's entry", func(t *testing.T) {
		var got map[string][]string
		if err := json.Unmarshal(JSONFor(ctx, a), &got); err != nil || !reflect.DeepEqual(got, map[string][]string{rtA: {"urgent"}}) {
			t.Fatalf("JSONFor(A) = %v (%v)", got, err)
		}
		if b := JSONFor(ctx, b); b != nil {
			t.Fatalf("JSONFor(B) = %s, want nil", b)
		}
	})

	t.Run("AG patch: a context without a filter filters nothing", func(t *testing.T) {
		bare := context.Background()
		if From(bare).Filtered(a) || JSONFor(bare, a) != nil || From(bare).JSON() != nil {
			t.Fatal("an unfiltered context must change nothing")
		}
		if With(bare, nil) != bare {
			t.Fatal("With(nil) should return the context unchanged")
		}
	})
}

func TestAGPatchClaimFilterCapabilities(t *testing.T) {
	if got := Capabilities(); !reflect.DeepEqual(got, []string{"claim_filter"}) {
		t.Fatalf("Capabilities() = %v", got)
	}
}
