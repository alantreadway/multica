package agent

// AG patch: rate_limit_event stream edge cases; TestAGPatch* as counted by
// ag-patches/test.sh.

import "testing"

func TestAGPatchRateLimitStreamShapes(t *testing.T) {
	const init = `{"type":"system","subtype":"init","session_id":"s1"}` + "\n"
	const result = `{"type":"result","subtype":"success","is_error":false,"session_id":"s1","result":"ok"}` + "\n"

	t.Run("AG patch: an event with its fields at the top level reaches the hook and parses", func(t *testing.T) {
		frames := init +
			`{"type":"rate_limit_event","status":"rejected","rateLimitType":"seven_day","resetsAt":1792821600}` + "\n" +
			result
		res, got := agRunClaudeStream(t, frames, true)
		if res.Status != "completed" || len(got) != 1 {
			t.Fatalf("status=%q hook calls=%d", res.Status, len(got))
		}
		ev, err := AGParseRateLimitEvent([]byte(got[0]))
		if err != nil || ev.Status != "rejected" || ev.RateLimitType != "seven_day" || ev.ResetsAt.Unix() != 1792821600 {
			t.Fatalf("event = %+v, err = %v", ev, err)
		}
	})

	t.Run("AG patch: every event of a run reaches the hook, in order", func(t *testing.T) {
		frames := init +
			`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","resetsAt":1792272600}}` + "\n" +
			`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1792272600}}` + "\n" +
			result
		_, got := agRunClaudeStream(t, frames, true)
		if len(got) != 2 {
			t.Fatalf("hook calls = %d, want 2", len(got))
		}
		first, err1 := AGParseRateLimitEvent([]byte(got[0]))
		second, err2 := AGParseRateLimitEvent([]byte(got[1]))
		if err1 != nil || err2 != nil || first.Status != "allowed" || second.Status != "rejected" {
			t.Fatalf("events = %+v / %+v, errs = %v / %v", first, second, err1, err2)
		}
	})

	t.Run("AG patch: a rate_limit_event with an unreadable info object is passed on and counts as unparseable", func(t *testing.T) {
		frames := init +
			`{"type":"rate_limit_event","rate_limit_info":"nope"}` + "\n" +
			result
		res, got := agRunClaudeStream(t, frames, true)
		if res.Status != "completed" || len(got) != 1 {
			t.Fatalf("status=%q hook calls=%d", res.Status, len(got))
		}
		if _, err := AGParseRateLimitEvent([]byte(got[0])); err == nil {
			t.Fatalf("payload %q parsed", got[0])
		}
	})
}
