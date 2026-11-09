package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

func newAGPoolPinnedTestCmd(t *testing.T, flags map[string]string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "update"}
	cmd.Flags().String("runtime-config", "", "")
	cmd.Flags().Bool("ag-pool-pinned", false, "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("profile", "", "")
	for k, v := range flags {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return cmd
}

func TestAGPatchRuntimePoolPinnedFlag(t *testing.T) {
	cases := []struct {
		label  string
		flags  map[string]string
		stored string // the agent's runtime_config on GET
		get    bool
		want   map[string]any
	}{
		{"pin keeps the other keys", map[string]string{"ag-pool-pinned": "true"}, `{"model_override":"m"}`, true,
			map[string]any{"model_override": "m", "ag_pool_pinned": true}},
		{"pin an agent with no runtime_config", map[string]string{"ag-pool-pinned": "true"}, `null`, true,
			map[string]any{"ag_pool_pinned": true}},
		{"unpin removes the key", map[string]string{"ag-pool-pinned": "false"}, `{"ag_pool_pinned":true,"model_override":"m"}`, true,
			map[string]any{"model_override": "m"}},
		{"merges into --runtime-config without a GET", map[string]string{"ag-pool-pinned": "true", "runtime-config": `{"x":1}`}, ``, false,
			map[string]any{"x": float64(1), "ag_pool_pinned": true}},
	}
	for _, c := range cases {
		t.Run("AG patch: "+c.label, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("MULTICA_AGENT_ID", "")
			t.Setenv("MULTICA_TASK_ID", "")
			var got map[string]any
			gets := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/agents/agent-1" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.Method {
				case http.MethodGet:
					gets++
					_, _ = w.Write([]byte(`{"id":"agent-1","runtime_config":` + c.stored + `}`))
				case http.MethodPut:
					var body map[string]json.RawMessage
					_ = json.NewDecoder(r.Body).Decode(&body)
					_ = json.Unmarshal(body["runtime_config"], &got)
					_, _ = w.Write([]byte(`{"id":"agent-1","name":"a"}`))
				}
			}))
			defer srv.Close()
			setCLITestServerEnv(t, srv.URL)

			cmd := newAGPoolPinnedTestCmd(t, c.flags)
			if _, err := captureStdout(t, func() error { return runAgentUpdate(cmd, []string{"agent-1"}) }); err != nil {
				t.Fatal(err)
			}
			if (gets == 1) != c.get {
				t.Fatalf("GET count = %d, want GET=%v", gets, c.get)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("runtime_config sent = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAGPatchRuntimePoolPinnedFlagRejectsNonObjectRuntimeConfig(t *testing.T) {
	cmd := newAGPoolPinnedTestCmd(t, map[string]string{"ag-pool-pinned": "true"})
	if err := agApplyPoolPinnedFlag(cmd, nil, "agent-1", map[string]any{"runtime_config": []any{1}}); err == nil {
		t.Fatal("AG patch: want an error for a non-object --runtime-config")
	}
}
