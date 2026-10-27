package protocol

// EventAGRuntimeTokenRate carries a runtime's live token rate to the
// workspace (AG patch). The server publishes it from heartbeats without
// storing it; see internal/agtokenrate.
const EventAGRuntimeTokenRate = "ag:runtime_token_rate"

// DaemonTokenRateReport is the live token rate a patched daemon attaches to
// each heartbeat of a runtime whose provider streams usage (Claude, Codex).
// Rates are tokens per second averaged over WindowSeconds, rounded to 0.1.
type DaemonTokenRateReport struct {
	// InputPerSec is uncached input plus cache writes: new context sent.
	InputPerSec     float64 `json:"input_per_sec"`
	OutputPerSec    float64 `json:"output_per_sec"`
	CacheReadPerSec float64 `json:"cache_read_per_sec"`
	WindowSeconds   int     `json:"window_seconds"`
	// ActiveRuns is how many runs on the runtime feed the rate now.
	ActiveRuns int `json:"active_runs"`
}
