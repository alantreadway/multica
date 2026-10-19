package main

import (
	"context"
	"net/http"
	"testing"
)

type routerLabel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

type routerLabelList struct {
	Labels []routerLabel `json:"labels"`
	Total  int           `json:"total"`
}

func routerLabelNames(labels []routerLabel) []string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return names
}

// PUT /api/labels/order must reach the reorder handler, not the /{id} update
// route, and the order it writes is what the list endpoint returns.
func TestLabelReorderThroughRouter(t *testing.T) {
	var ids []string
	for _, name := range []string{"router-b", "router-a", "router-c"} {
		resp := authRequest(t, "POST", "/api/labels", map[string]any{
			"resource_type": "skill", "name": name, "color": "#3b82f6",
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: got %d", name, resp.StatusCode)
		}
		var l routerLabel
		readJSON(t, resp, &l)
		ids = append(ids, l.ID)
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE id = $1`, l.ID)
		})
	}

	// New labels append: creation order, not alphabetical.
	resp := authRequest(t, "GET", "/api/labels?resource_type=skill", nil)
	var list routerLabelList
	readJSON(t, resp, &list)
	if got := routerLabelNames(list.Labels); len(got) != 3 || got[0] != "router-b" || got[1] != "router-a" || got[2] != "router-c" {
		t.Fatalf("list after create = %v, want creation order", got)
	}

	resp = authRequest(t, "PUT", "/api/labels/order", map[string]any{
		"resource_type": "skill", "label_ids": []string{ids[2], ids[0], ids[1]},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/labels/order: got %d, want 200", resp.StatusCode)
	}
	var reordered routerLabelList
	readJSON(t, resp, &reordered)
	if got := routerLabelNames(reordered.Labels); len(got) != 3 || got[0] != "router-c" || got[1] != "router-b" || got[2] != "router-a" {
		t.Fatalf("reorder response = %v, want [router-c router-b router-a]", got)
	}
	for i, l := range reordered.Labels {
		if l.Position != i {
			t.Errorf("%s position = %d, want %d", l.Name, l.Position, i)
		}
	}

	resp = authRequest(t, "GET", "/api/labels?resource_type=skill", nil)
	readJSON(t, resp, &list)
	if got := routerLabelNames(list.Labels); len(got) != 3 || got[0] != "router-c" || got[1] != "router-b" || got[2] != "router-a" {
		t.Fatalf("list after reorder = %v", got)
	}

	// A stale set is a 409 over HTTP too, and leaves the order alone.
	resp = authRequest(t, "PUT", "/api/labels/order", map[string]any{
		"resource_type": "skill", "label_ids": []string{ids[0]},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale reorder: got %d, want 409", resp.StatusCode)
	}
	resp = authRequest(t, "GET", "/api/labels?resource_type=skill", nil)
	readJSON(t, resp, &list)
	if got := routerLabelNames(list.Labels); got[0] != "router-c" {
		t.Fatalf("stale reorder changed the order: %v", got)
	}
}
