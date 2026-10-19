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
