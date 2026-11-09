package daemon

import (
	"path/filepath"
	"testing"
)

func TestAGPatchRuntimePoolFromEnv(t *testing.T) {
	cases := []struct {
		env     map[string]string
		want    string
		wantErr bool
	}{
		{env: map[string]string{}, want: ""},
		{env: map[string]string{"MULTICA_AG_POOL": "  "}, want: ""},
		{env: map[string]string{"MULTICA_AG_POOL": " alan "}, want: "alan"},
		{env: map[string]string{"MULTICA_AG_POOL": "home.lab_2-a"}, want: "home.lab_2-a"},
		{env: map[string]string{"MULTICA_AG_POOL": "two words"}, wantErr: true},
		{env: map[string]string{"MULTICA_AG_POOL": "-leading"}, wantErr: true},
	}
	for _, c := range cases {
		got, err := agPoolFromEnv(func(k string) (string, bool) { v, ok := c.env[k]; return v, ok })
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("AG patch: env %v: got %q, %v; want %q, error %v", c.env, got, err, c.want, c.wantErr)
		}
	}
}

func TestAGPatchRuntimePoolRegisterRequest(t *testing.T) {
	req := map[string]any{"daemon_id": "d"}
	agAddPoolToRegister(req, "")
	if _, ok := req["ag_pool"]; ok || len(req) != 1 {
		t.Fatalf("AG patch: no pool must leave the upstream request alone, got %v", req)
	}
	agAddPoolToRegister(req, "alan")
	if req["ag_pool"] != "alan" {
		t.Fatalf("AG patch: ag_pool = %v, want alan", req["ag_pool"])
	}
}

// A pooled follow-up claimed on the other machine carries the first machine's
// work_dir as PriorWorkDir (both machines share HOME, so the path even looks
// local). The daemon must not reuse it: the directory does not exist here, or
// exists but belongs to another agent's run.
func TestAGPatchRuntimePoolForeignPriorWorkDirIgnored(t *testing.T) {
	root := t.TempDir()
	task := leaderReuseTestTask("task-pool-follow-up")
	task.IsLeaderTask = false

	own := filepath.Join(root, "ws-leader", "aaaa1111", "workdir")
	writeLeaderTaskMarker(t, own, "agent-leader", "issue-leader")
	writeLeaderManagedEnvProvenance(t, own, "ws-leader", "issue-leader", "agent-leader")
	task.PriorWorkDir = own
	if _, ok := shouldReusePriorWorkdir(task, nil, root); !ok {
		t.Fatalf("AG patch: setup: this machine's own workdir %q was not reusable", own)
	}

	task.PriorWorkDir = filepath.Join(root, "ws-leader", "bbbb2222", "workdir")
	if dir, ok := shouldReusePriorWorkdir(task, nil, root); ok {
		t.Fatalf("AG patch: reused %q, a workdir that exists only on the other machine", dir)
	}

	clash := filepath.Join(root, "ws-leader", "cccc3333", "workdir")
	writeLeaderTaskMarker(t, clash, "agent-other", "issue-other")
	writeLeaderManagedEnvProvenance(t, clash, "ws-leader", "issue-other", "agent-other")
	task.PriorWorkDir = clash
	if dir, ok := shouldReusePriorWorkdir(task, nil, root); ok {
		t.Fatalf("AG patch: reused %q, a local directory of another agent's run at the same path", dir)
	}
}
