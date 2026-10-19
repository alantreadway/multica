package taskfailure

import (
	"fmt"
	"testing"
	"time"
)

// The CLI wording these fixtures use is unverified against the current Claude
// Code release; they record the forms the parser accepts.

func TestAGPatchQuotaResetEpoch(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	want := time.Date(2026, 10, 17, 18, 0, 0, 0, time.UTC)
	for name, text := range map[string]string{
		"seconds":      fmt.Sprintf("Claude AI usage limit reached|%d", want.Unix()),
		"milliseconds": fmt.Sprintf("Claude AI usage limit reached|%d", want.UnixMilli()),
		"wrapped":      fmt.Sprintf("claude exited: Usage limit reached|%d\n", want.Unix()),
	} {
		got, ok := AGQuotaResetTime(text, now, time.UTC)
		if !ok || !got.Equal(want) {
			t.Errorf("%s: got %v ok=%v, want %v", name, got, ok, want)
		}
	}
}

func TestAGPatchQuotaResetClockTime(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	// 2026-10-17 12:00 UTC is 13:00 BST.
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		text string
		loc  *time.Location
		want time.Time
	}{
		{"zone later today", "You've hit your limit · resets 9pm (Europe/London)", time.UTC, time.Date(2026, 10, 17, 21, 0, 0, 0, london)},
		{"zone already passed rolls to tomorrow", "resets 9am (Europe/London)", time.UTC, time.Date(2026, 10, 18, 9, 0, 0, 0, london)},
		{"minutes", "resets 9:30pm (Europe/London)", time.UTC, time.Date(2026, 10, 17, 21, 30, 0, 0, london)},
		{"no zone uses the server zone", "resets 10pm", tokyo, time.Date(2026, 10, 17, 22, 0, 0, 0, tokyo)},
		{"unknown zone uses the server zone", "resets 10pm (Mars/Olympus)", tokyo, time.Date(2026, 10, 17, 22, 0, 0, 0, tokyo)},
		{"12am is midnight", "resets 12am (Europe/London)", time.UTC, time.Date(2026, 10, 18, 0, 0, 0, 0, london)},
		{"12pm is noon", "resets 12pm (Europe/London)", time.UTC, time.Date(2026, 10, 18, 12, 0, 0, 0, london)},
		{"case and spacing", "Resets 9 PM (Europe/London)", time.UTC, time.Date(2026, 10, 17, 21, 0, 0, 0, london)},
	}
	for _, tc := range cases {
		got, ok := AGQuotaResetTime(tc.text, now, tc.loc)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("%s: got %v ok=%v, want %v", tc.name, got, ok, tc.want)
		}
	}
}

func TestAGPatchQuotaResetGarbage(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, text := range []string{
		"",
		"usage limit reached",
		"usage limit reached|soon",
		"usage limit reached|123",
		"resets 25pm",
		"resets 0am",
		"resets 9:75pm",
		"resets tomorrow",
		"rate limited",
	} {
		if got, ok := AGQuotaResetTime(text, now, time.UTC); ok {
			t.Errorf("%q: parsed %v, want no reset", text, got)
		}
	}
}

func TestAGPatchQuotaBillingFailure(t *testing.T) {
	for text, want := range map[string]bool{
		"API Error: 402 Payment Required":                  true,
		"insufficient_balance":                             true,
		"Your credit balance is too low to access the API": true,
		"You have run out of credits":                      true,
		"Claude AI usage limit reached|1760000000":         false,
		"You've hit your limit · resets 9pm":               false,
		"error code 14020 from proxy":                      false,
		"":                                                 false,
	} {
		if got := AGIsBillingFailure(text); got != want {
			t.Errorf("AGIsBillingFailure(%q) = %v, want %v", text, got, want)
		}
	}
}

// "Next occurrence after now": a clock time equal to now has already passed, and
// the epoch form wins when a message carries both.
func TestAGPatchQuotaResetNextOccurrence(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	got, ok := AGQuotaResetTime("resets 12pm", now, time.UTC)
	if want := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("clock time equal to now: got %v ok=%v, want %v", got, ok, want)
	}
	got, ok = AGQuotaResetTime("resets 12:01pm", now, time.UTC)
	if want := time.Date(2026, 10, 17, 12, 1, 0, 0, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("a minute ahead: got %v ok=%v, want %v", got, ok, want)
	}
	got, ok = AGQuotaResetTime("usage limit reached|1792000000 resets 9pm (Europe/London)", now, time.UTC)
	if want := time.Unix(1792000000, 0); !ok || !got.Equal(want) {
		t.Errorf("epoch and clock time together: got %v ok=%v, want the epoch %v", got, ok, want)
	}
}

// The seconds/milliseconds split sits at 1e12: the largest 12-digit value is
// still seconds, the first 13-digit one is milliseconds.
func TestAGPatchQuotaResetEpochUnitBoundary(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	got, ok := AGQuotaResetTime("usage limit reached|999999999999", now, time.UTC)
	if want := time.Unix(999999999999, 0); !ok || !got.Equal(want) {
		t.Errorf("12 digits: got %v ok=%v, want %v as seconds", got, ok, want)
	}
	got, ok = AGQuotaResetTime("usage limit reached|1760000000123", now, time.UTC)
	if want := time.UnixMilli(1760000000123); !ok || !got.Equal(want) {
		t.Errorf("13 digits: got %v ok=%v, want %v as milliseconds", got, ok, want)
	}
}

// Billing wording must be recognised even when it sits inside a longer message,
// but a window-limit message that merely contains digits stays retryable.
func TestAGPatchQuotaBillingFailureInContext(t *testing.T) {
	for text, want := range map[string]bool{
		"claude exited with status 1: API Error: 402 {\"type\":\"error\"}": true,
		"Credit balance is too low":                                        true,
		"Usage limit reached|1760004020":                                   false,
		"Usage limit reached|1760000000 (retry in 4020 seconds)":           false,
		"5-hour limit reached \u00b7 resets 9pm (Europe/London)":           false,
	} {
		if got := AGIsBillingFailure(text); got != want {
			t.Errorf("AGIsBillingFailure(%q) = %v, want %v", text, got, want)
		}
	}
}
