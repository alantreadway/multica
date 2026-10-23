package taskfailure

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AG patch: deferred retry until quota reset (see ag-patches/PATCHES.md).
//
// A task killed by a provider usage-window limit is retried when the window
// resets. These helpers read the reset time out of the failure text and tell a
// window limit from an out-of-credit billing failure, which must never be
// retried. The CLI wording is not verified against the current Claude Code
// release; the fixtures in ag_quota_reset_test.go record the forms handled.

var (
	// "usage limit reached|1760000000": epoch seconds, or milliseconds.
	agQuotaEpochRe = regexp.MustCompile(`(?i)usage limit reached\|(\d{9,14})`)
	// "resets 9pm (Europe/London)", "resets 9:30 am". The zone is optional.
	agQuotaResetsRe = regexp.MustCompile(`(?i)resets\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)(?:\s*\(([^)]+)\))?`)
)

// agQuotaMsEpochThreshold separates epoch milliseconds from epoch seconds: no
// seconds value reaches 1e12 for another 30,000 years.
const agQuotaMsEpochThreshold = 1e12

// AGQuotaResetTime returns the quota reset time named in a failure message, and
// false when it names none. The clock-time form means the next occurrence
// after now in the stated IANA zone, or in loc when the zone is absent or
// unknown.
func AGQuotaResetTime(text string, now time.Time, loc *time.Location) (time.Time, bool) {
	if m := agQuotaEpochRe.FindStringSubmatch(text); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		if float64(n) > agQuotaMsEpochThreshold {
			return time.UnixMilli(n), true
		}
		return time.Unix(n, 0), true
	}
	m := agQuotaResetsRe.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	hour, _ := strconv.Atoi(m[1])
	if hour < 1 || hour > 12 {
		return time.Time{}, false
	}
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
		if minute > 59 {
			return time.Time{}, false
		}
	}
	hour %= 12
	if strings.EqualFold(m[3], "pm") {
		hour += 12
	}
	zone := loc
	if name := strings.TrimSpace(m[4]); name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			zone = l
		}
	}
	if zone == nil {
		zone = time.Local
	}
	local := now.In(zone)
	reset := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, zone)
	if !reset.After(now) {
		reset = time.Date(local.Year(), local.Month(), local.Day()+1, hour, minute, 0, 0, zone)
	}
	return reset, true
}

// AGIsBillingFailure reports whether failure text is an out-of-credit billing
// failure rather than a usage-window limit. Waiting does not fix billing, so
// these are not retried.
func AGIsBillingFailure(text string) bool {
	lower := strings.ToLower(text)
	return httpQuotaCodeRe.MatchString(lower) ||
		containsAny(lower, "insufficient_balance", "credits", "balance is too low")
}
