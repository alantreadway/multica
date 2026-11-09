package agpool

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
)

func TestAGPatchRuntimePoolNormalize(t *testing.T) {
	for in, want := range map[string]struct {
		name string
		ok   bool
	}{
		"":                       {"", true},
		"  alan  ":               {"alan", true},
		"a.b_c-1":                {"a.b_c-1", true},
		"_leading":               {"", false},
		"has space":              {"", false},
		"emoji-🙂":                {"", false},
		string(make([]byte, 65)): {"", false},
	} {
		name, ok := Normalize(in)
		if ok != want.ok || (ok && name != want.name) {
			t.Errorf("AG patch: Normalize(%q) = %q, %v; want %q, %v", in, name, ok, want.name, want.ok)
		}
	}
}

func TestAGPatchRuntimePoolWithMetadata(t *testing.T) {
	upstream := []byte(`{"version":"1.0.0","cli_version":"0.8.1"}`)
	if got := WithMetadata(upstream, ""); !bytes.Equal(got, upstream) {
		t.Fatalf("AG patch: no pool changed the metadata: %s", got)
	}
	m := map[string]any{}
	if err := json.Unmarshal(WithMetadata(upstream, "alan"), &m); err != nil {
		t.Fatal(err)
	}
	if m[MetadataKey] != "alan" || m["version"] != "1.0.0" || m["cli_version"] != "0.8.1" {
		t.Fatalf("AG patch: metadata = %v", m)
	}
}

func TestAGPatchRuntimePoolContextSet(t *testing.T) {
	a := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	b := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	if From(context.Background()).Has(a) {
		t.Fatal("AG patch: an empty context has a pooled runtime")
	}
	ctx := WithSet(context.Background(), NewSet([]pgtype.UUID{a}))
	if !From(ctx).Has(a) || From(ctx).Has(b) {
		t.Fatal("AG patch: set membership wrong")
	}
}
