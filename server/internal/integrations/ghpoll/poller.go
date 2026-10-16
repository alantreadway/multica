package ghpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	perPage = 100
	// maxPages bounds one repo's catch-up in a single tick. Past it the cursor
	// still advances: a repo that changed 1000 PRs between two polls is an
	// import, not a steady state.
	maxPages = 10
	// maxPause caps how long a rate-limit response can park the poller.
	maxPause = time.Hour
	// cursorSkew rewinds the cursor past a PR that failed to mirror, so it is
	// picked up again. GitHub timestamps have one-second resolution.
	cursorSkew = time.Second
)

// PR is one polled pull request.
type PR struct {
	// Repo is the canonical owner/name GitHub reports for the PR's base repo.
	Repo      RepoRef
	Number    int32
	UpdatedAt time.Time
	// Raw is the PR object exactly as GitHub's list endpoint returned it.
	Raw json.RawMessage
}

// Sink receives what the poller finds.
type Sink interface {
	// Mirror records pr in every listed workspace. It must try every workspace
	// even when one fails, and return an error when any did, so the poller
	// retries the PR on the next tick.
	Mirror(ctx context.Context, workspaces []pgtype.UUID, pr PR) error
	// Refresh asks for the PR's CI and mergeability snapshot. Called once per
	// PR after a successful Mirror. Never blocks.
	Refresh(repo RepoRef, number int32)
}

type repoState struct {
	etag   string
	cursor time.Time // max updated_at already mirrored; zero before the first pass
}

// Poller runs the poll loop. Create it with New; it is not safe for concurrent
// use beyond Run.
type Poller struct {
	cfg  Config
	src  Source
	sink Sink

	httpClient *http.Client
	now        func() time.Time

	state       map[string]*repoState
	pausedUntil time.Time
}

// New builds a Poller.
func New(cfg Config, src Source, sink Sink) *Poller {
	if cfg.APIBase == "" {
		cfg.APIBase = defaultAPI
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	return &Poller{
		cfg:        cfg,
		src:        src,
		sink:       sink,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		now:        time.Now,
		state:      map[string]*repoState{},
	}
}

// Run polls immediately and then every Interval until ctx ends. A panic in one
// tick is logged and the loop carries on.
func (p *Poller) Run(ctx context.Context) {
	p.safeTick(ctx)
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.safeTick(ctx)
		}
	}
}

func (p *Poller) safeTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("ghpoll: tick panicked, continuing", "panic", fmt.Sprint(r))
		}
	}()
	p.Tick(ctx)
}

// Tick polls every watched repo once.
func (p *Poller) Tick(ctx context.Context) {
	if until := p.pausedUntil; p.now().Before(until) {
		slog.Debug("ghpoll: paused by rate limit", "until", until)
		return
	}
	watches, err := p.src.Watched(ctx)
	if err != nil {
		slog.Warn("ghpoll: list watched repos failed", "err", err)
		return
	}
	authLogged := false
	for _, w := range watches {
		if ctx.Err() != nil || p.now().Before(p.pausedUntil) {
			return
		}
		err := p.pollRepo(ctx, w)
		var se *statusError
		var rl *rateLimitedError
		switch {
		case err == nil:
		case errors.As(err, &rl):
			p.pausedUntil = rl.until
			slog.Warn("ghpoll: rate limited, pausing", "repo", w.Repo.key(), "until", rl.until)
			return
		case errors.As(err, &se) && (se.status == http.StatusUnauthorized || se.status == http.StatusForbidden):
			if !authLogged {
				authLogged = true
				slog.Error("ghpoll: GitHub rejected the poll token, check GITHUB_POLL_TOKEN and its repo access", "repo", w.Repo.key(), "status", se.status)
			}
		case errors.As(err, &se):
			slog.Warn("ghpoll: poll failed", "repo", w.Repo.key(), "status", se.status)
		default:
			slog.Warn("ghpoll: poll failed", "repo", w.Repo.key(), "err", err)
		}
	}
}

type statusError struct{ status int }

func (e *statusError) Error() string { return fmt.Sprintf("github status %d", e.status) }

type rateLimitedError struct{ until time.Time }

func (e *rateLimitedError) Error() string { return "github rate limited" }

type listItem struct {
	Number    int32  `json:"number"`
	UpdatedAt string `json:"updated_at"`
	Base      struct {
		Repo struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repo"`
	} `json:"base"`
}

func (p *Poller) pollRepo(ctx context.Context, w Watch) error {
	key := w.Repo.key()
	st := p.state[key]
	if st == nil {
		st = &repoState{}
		p.state[key] = st
	}
	first := st.cursor.IsZero()

	var collected []PR
	stop := false
	newETag := ""
	for page := 1; page <= maxPages && !stop; page++ {
		etag := ""
		if page == 1 {
			etag = st.etag
		}
		status, header, body, err := p.get(ctx, w.Repo, page, etag)
		if err != nil {
			return err
		}
		if status == http.StatusNotModified {
			return nil
		}
		var raws []json.RawMessage
		if err := json.Unmarshal(body, &raws); err != nil {
			return errors.New("malformed pull request list")
		}
		if page == 1 {
			newETag = header.Get("ETag")
		}
		for _, raw := range raws {
			var it listItem
			if json.Unmarshal(raw, &it) != nil || it.Number == 0 {
				continue
			}
			updated, err := time.Parse(time.RFC3339, it.UpdatedAt)
			if err != nil {
				continue
			}
			// Equal to the cursor is still a candidate: GitHub timestamps have
			// one-second resolution, so a PR changed later in the cursor's
			// second would otherwise be missed. The upsert makes a repeat safe.
			if !first && updated.Before(st.cursor) {
				stop = true
				break
			}
			repo := w.Repo
			if it.Base.Repo.Name != "" && it.Base.Repo.Owner.Login != "" {
				repo = RepoRef{Owner: it.Base.Repo.Owner.Login, Name: it.Base.Repo.Name}
			}
			collected = append(collected, PR{Repo: repo, Number: it.Number, UpdatedAt: updated, Raw: raw})
		}
		if first || len(raws) < perPage || !hasNextPage(header) {
			break
		}
	}

	// Oldest first, so a failure part-way leaves the older PRs done.
	var failedMin, newest time.Time
	failed := false
	for i := len(collected) - 1; i >= 0; i-- {
		pr := collected[i]
		if err := p.sink.Mirror(ctx, w.Workspaces, pr); err != nil {
			slog.Warn("ghpoll: mirror failed, will retry", "repo", key, "number", pr.Number, "err", err)
			if !failed || pr.UpdatedAt.Before(failedMin) {
				failedMin = pr.UpdatedAt
			}
			failed = true
			continue
		}
		p.sink.Refresh(pr.Repo, pr.Number)
		if pr.UpdatedAt.After(newest) {
			newest = pr.UpdatedAt
		}
	}
	switch {
	case failed:
		// Re-list next tick: a stored ETag would answer 304 for the same page.
		st.cursor = failedMin.Add(-cursorSkew)
		st.etag = ""
	default:
		if newest.After(st.cursor) {
			st.cursor = newest
		}
		st.etag = newETag
	}
	return nil
}

// get fetches one page of the repo's pulls, newest update first. A 304 returns
// status 304 and no body. Anything but 200/304 is an error.
func (p *Poller) get(ctx context.Context, repo RepoRef, page int, etag string) (int, http.Header, []byte, error) {
	q := url.Values{}
	q.Set("state", "all")
	q.Set("sort", "updated")
	q.Set("direction", "desc")
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls?%s",
		strings.TrimRight(p.cfg.APIBase, "/"), url.PathEscape(repo.Owner), url.PathEscape(repo.Name), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+p.cfg.Token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		// The token only ever travels in a header, which net/http keeps out of
		// its errors.
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	remainingZero := strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0"
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotModified:
		if remainingZero {
			// This answer was the last one in the window; park until it resets.
			p.pausedUntil = p.rateLimitUntil(resp.Header)
		}
		return resp.StatusCode, resp.Header, body, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		if remainingZero || resp.Header.Get("Retry-After") != "" ||
			strings.Contains(strings.ToLower(string(body)), "rate limit") {
			return 0, nil, nil, &rateLimitedError{until: p.rateLimitUntil(resp.Header)}
		}
	}
	return 0, nil, nil, &statusError{status: resp.StatusCode}
}

// rateLimitUntil is when to resume: Retry-After, else X-RateLimit-Reset, else a
// minute from now; never more than maxPause away.
func (p *Poller) rateLimitUntil(h http.Header) time.Time {
	now := p.now()
	until := now.Add(time.Minute)
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			until = now.Add(time.Duration(secs) * time.Second)
		}
	} else if v := strings.TrimSpace(h.Get("X-RateLimit-Reset")); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			if t := time.Unix(unix, 0); t.After(now) {
				until = t
			}
		}
	}
	if limit := now.Add(maxPause); until.After(limit) {
		until = limit
	}
	return until
}

func hasNextPage(h http.Header) bool {
	for _, v := range h.Values("Link") {
		for _, part := range strings.Split(v, ",") {
			if strings.Contains(part, `rel="next"`) {
				return true
			}
		}
	}
	return false
}
