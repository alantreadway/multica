package ghpoll

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RepoRef is a GitHub owner/repo pair.
type RepoRef struct {
	Owner string
	Name  string
}

func (r RepoRef) key() string { return strings.ToLower(r.Owner + "/" + r.Name) }

// Watch is one repository and every workspace that lists it.
type Watch struct {
	Repo       RepoRef
	Workspaces []pgtype.UUID
}

// Source lists the repositories to poll.
type Source interface {
	Watched(ctx context.Context) ([]Watch, error)
}

var (
	// https://github.com/o/r[.git][/...] and ssh://git@github.com/o/r[.git]
	urlRepoRe = regexp.MustCompile(`^/([^/]+)/([^/]+?)(?:\.git)?/?$`)
	// git@github.com:o/r[.git]
	scpRepoRe = regexp.MustCompile(`^[^@/]+@(?:www\.)?github\.com:([^/]+)/([^/]+?)(?:\.git)?/?$`)
)

// ParseGitHubRepo extracts owner/repo from a git URL on github.com. Anything
// else (another host, an unparseable string) reports false.
func ParseGitHubRepo(raw string) (RepoRef, bool) {
	raw = strings.TrimSpace(raw)
	if m := scpRepoRe.FindStringSubmatch(raw); m != nil {
		return RepoRef{Owner: m[1], Name: m[2]}, true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return RepoRef{}, false
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return RepoRef{}, false
	}
	m := urlRepoRe.FindStringSubmatch(u.Path)
	if m == nil {
		return RepoRef{}, false
	}
	return RepoRef{Owner: m[1], Name: m[2]}, true
}

// WorkspaceRepos is one workspace's raw `repos` JSONB.
type WorkspaceRepos struct {
	WorkspaceID pgtype.UUID
	Repos       []byte
}

// GroupWatches turns workspace repo lists into one Watch per distinct
// (case-insensitive) GitHub repo, in a stable order. Entries that are not on
// GitHub or cannot be parsed are skipped silently.
func GroupWatches(rows []WorkspaceRepos) []Watch {
	byKey := map[string]*Watch{}
	for _, row := range rows {
		var entries []struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(row.Repos, &entries) != nil {
			continue
		}
		for _, e := range entries {
			ref, ok := ParseGitHubRepo(e.URL)
			if !ok {
				continue
			}
			w := byKey[ref.key()]
			if w == nil {
				w = &Watch{Repo: ref}
				byKey[ref.key()] = w
			}
			if !containsUUID(w.Workspaces, row.WorkspaceID) {
				w.Workspaces = append(w.Workspaces, row.WorkspaceID)
			}
		}
	}
	out := make([]Watch, 0, len(byKey))
	for _, w := range byKey {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo.key() < out[j].Repo.key() })
	return out
}

func containsUUID(list []pgtype.UUID, id pgtype.UUID) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

// Querier is the slice of a pgx pool PGSource needs.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// PGSource reads every workspace's `repos` column (migration 014).
type PGSource struct{ DB Querier }

// Watched implements Source.
func (s PGSource) Watched(ctx context.Context) ([]Watch, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, repos FROM workspace WHERE repos <> '[]'::jsonb`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []WorkspaceRepos
	for rows.Next() {
		var wr WorkspaceRepos
		if err := rows.Scan(&wr.WorkspaceID, &wr.Repos); err != nil {
			return nil, err
		}
		all = append(all, wr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return GroupWatches(all), nil
}
