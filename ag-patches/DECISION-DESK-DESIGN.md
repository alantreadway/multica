# Decision Desk: design

Status: phase 0 shipped (`ag-jira-sync` `df308e4`); phase 1 is built and tested but not deployed (the engine is on `ag-jira-sync` branch `desk-phase1`). Section 1's numbers are from read-only checks on
2026-11-05, before phase 0.

**Goal:** one screen that lists everything stuck on Alan, offers one-tap actions, lets low-risk items
progress on their own, and logs why.

**UI decision (Alan):** a page in the Multica fork, with a `Decisions` nav item directly under Inbox.
Section 3 has the detail.

## 1. What the live data says

82 open Multica issues. Only 19 are real decisions for Alan today.

| Bucket | Count | Issues | Who acts |
|---|---|---|---|
| Decisions | 19 | see below | Alan |
| CRT mirrors flagged closed in JIRA (`rt_closed_noted`), still at `backlog` | 26 | MAG-910, MAG-894, ... | nobody |
| Review queue items whose PR is red or has open feedback | 4 | MAG-972, MAG-959, MAG-920, MAG-958 | the PR author |
| Queue duplicate: AG-13282 ships in the same PR as AG-18615 (#8954) | 1 | MAG-971 | nobody |
| Queue item whose ticket has already gone back to `In Progress` | 1 | MAG-961 | AIW |
| Waiting on a colleague's review | 2 | MAG-935, MAG-954 | colleague |
| Idle over 3 days at `handoff` | 2 | MAG-813, MAG-805 | unclear |
| Agents working (`in_progress`) | 12 | | agents |
| Other `backlog` (includes 5 parked reviews) | 15 | | later |

The 19 decisions:

| Type | Count | Issues |
|---|---|---|
| AIW escalation: ticket at `Needs Review`, no PR | 6 | MAG-936, 962, 963, 966, 967, 968 |
| Agent question (`needs-decision`) | 4 | MAG-929, 898, 916, 797 |
| Bot PR, CI green, Codex clean on the head | 3 | MAG-964 (#8960), MAG-965 (#8961), MAG-974 (#8966) |
| Colleague PR needing a real review, CI green | 3 | MAG-970 (#8954), MAG-780 (#8833), MAG-906 (#8911) |
| Report with proposed fixes | 3 | MAG-957, MAG-947, MAG-932 |

Four findings change the design:

1. **Auto-merge will not fix the volume yet.** None of today's 18 queue items passes a strict low-risk
   rule. All three bot PRs carry `Breaking`, and #8961's summary says the break is real (a default series
   id changes). Most of the relief comes from removing noise and batching.
2. **The "no PR" items are not reviews.** They are AIW escalations. Job 3 titles them `Review AG-…`, tells
   the reader to review a PR that does not exist, and summarises the ticket instead of AIW's question. The
   real ask sits in the `jira-ai-bot` comment, with a parseable shape: `Do next:`, `Q1`, and
   `Pipeline signals` (`Decision`, `Auto-proceed`). Confirmed on all six, which are three different asks: three
   plan approvals (AG-18666, AG-18874, AG-18374), two blocked on a question (AG-18873, AG-15293) and one
   verification gate waiting on sub-tasks (AG-19149).
3. **GitHub will not protect a merge here.** `latest` needs 1 approval and the `report` check, but it does
   not dismiss stale approvals on push and does not require threads to be resolved. The executor has to
   enforce both.
4. **`codex: ✅` is not enough evidence.** A squad review on #8939 found the Codex summary was stale,
   because a later commit had not been reviewed. Trust only the `<!-- codex-pr-review sha=… -->` marker when
   the sha equals the head, plus zero unresolved Codex threads.

History, for scale: of the latest 100 merged `app/ag-jira-agent-ci` PRs, Alan merged 60. Created-to-merged
is 4.5 h at p50 and 140 h at p90. 35 of the 100 changed 50 lines or fewer, which caps a "tiny PR" lane at
about a third of bot PRs before other vetoes.

## 2. Architecture

Same four parts under every UI option:

```
Sources: Multica, GitHub, JIRA (the reads ag-jira-sync already makes)
  -> 1. Detect     who is this waiting on? (deterministic)
  -> 2. Recommend  which action, under which policy? (rules; the model can only veto)
  -> 3. Act        one tap or auto, re-checked at execution time
  -> 4. Log        append-only record, plus a /note on the issue
```

### 1. Detect: "dead in the water"

An issue is stuck when nothing automated is pending: no queued or running run, no pending wake-up, CI not
running, and past a short settle window. These are the reads jobs 10, 14 and 15 already make.

Each stuck issue gets `waiting_on`: `alan`, `author`, `colleague`, `agent` or `nobody`. The desk opens on
`alan`. The others show as counts and only surface once idle past a threshold, with a nudge action.

### 2. Recommend: card types and quick actions

| Card | Shown on the card | Quick actions |
|---|---|---|
| AIW plan approval | Plan headline, `Do next` line, decision and confidence | Approve (move to `In Progress`); `Re-plan` with a note; open ticket |
| AIW blocked | Status, Q1 text, decision and confidence | Do the suggested close; answer Q1 (canned yes/no, or free text) and move to `In Progress`; open ticket |
| Agent question | The agent's (a)/(b)/(c) options, default marked | One button per option (posts the reply, which wakes the agent); free reply |
| Bot PR | Size, CI, Codex at head, class labels, what stops auto | Approve and merge; approve only; ask Peer Squad; send back with a note; snooze until new head |
| Colleague PR | Haiku summary, squad verdict, diff link | Ask squad; open diff; snooze until new head. **Never one-tap merged** |
| Report fixes | Proposal rows | Apply per row, or apply all of one kind (e.g. stale fixVersions) |
| Stale or waiting | Idle time, who holds it | Wake holder; draft a Slack nudge (never auto-sent); cancel |

Cards sort quickest-to-clear first, grouped so that same-shape items can be cleared as a batch. Keyboard:
`j`/`k` to move, a number key for the action.

Two small upstream changes make cards reliable:

- Agents emit a decision block (`<!-- decision: {question, options, default} -->`) so buttons do not depend
  on parsing prose. Prose parsing stays as the fallback, since MAG-929's format already nearly fits.
- Report autopilots emit their rows as JSON beside the table.

### 3. Act: autonomy levels

Each action has its own level, in a versioned `policy.json`:

| Level | Behaviour |
|---|---|
| `off` | Card only |
| `shadow` | Logs what it would do. Alan still decides, and the two are compared |
| `one_tap` | Recommended button, Alan clicks |
| `auto_hold` | Runs after a cancel window (start at 30 min), visible in the Auto lane |
| `auto` | Runs at the next poll, for reversible housekeeping only |

Starting levels:

| Action | Start | Reason |
|---|---|---|
| Park an author's-turn item until CI is green or there is a new head | `auto` | Reversible; job 3 already parks drafts |
| Cancel a mirror for a closed CRT ticket | `auto`, after a go-ahead | Reversible, but job 16 today promises "nothing has been cancelled" |
| Close a queue duplicate | `one_tap` | Matching on one PR may be wrong for a ticket with other scope |
| Apply a stale fixVersion from a report | `one_tap`, later `auto` | Writes to JIRA |
| AIW "close as delivered" | `one_tap` | Closes a ticket on a model's claim |
| Approve and merge a bot PR | `shadow`, then `one_tap`, then `auto_hold` | Hard to reverse, public repo |
| Colleague PR, release-branch base, product questions, `--admin` | never automatic | Judgement, or an explicit standing rule |

**First merge policy, `aiw-tiny`.** All must hold on the current head; any failure means no auto, with the
reason shown on the card:

1. Author is `app/ag-jira-agent-ci`. Base is `latest` (not `bX.Y.Z`, not another PR's branch).
2. The branch is in this repo, and every push to it, per GitHub's activity log, is by app id `286720198`.
3. 50 changed lines or fewer, 5 files or fewer, and every file under `packages/ag-charts-{community,enterprise,core}/src/`.
   No file under a `license/` directory. No added line uses `eval`, `new Function`, raw HTML sinks, network calls,
   `postMessage` or `document.cookie`, or contains hidden Unicode. The scan catches plain forms only: it is a speed bump.
4. `report` passes on the head, with at least one check that actually succeeded.
   CodeQL also passes on the head, published by `github-advanced-security`, which a workflow on the PR's branch cannot
   publish as.
5. The `github-actions[bot]` Codex summary comment's stamp equals the head, its summary line says no issues found, and
   there are zero unresolved Codex threads. A finding may never become a thread, and a thread can be resolved unfixed.
6. That comment's classification line exists for this head: `content` is `library`, `change` is `mechanical` or
   `small-fix`, and `risky`, `breaking` and `complex` are false. Any `Breaking`, `Risky` or `Complex` label also refuses.
7. No change request, no person's comment on the PR, no colleague comment hold on the ticket.
8. Ticket is at `Needs Review`, and AIW's `Auto-proceed` signal does not say no.

**Lock-down.** A PR is untrusted input until proven otherwise, and every fact above needs a source it cannot forge.

| Fact | Approved source | Not accepted, and why |
|---|---|---|
| Classification | `<!-- codex-pr-review-class -->` at line 3 of the summary comment, from ag-dev-prompts #1458, for this head | Multica labels and Haiku: anything can edit them, and Haiku reads PR text. They can only refuse |
| Codex reviewed the head | `sha=` at line 2 of a comment by `github-actions[bot]` | The same text from anyone else, or lower in the comment, where it is model output |
| Who wrote the PR | App id for the bot. For a person, the collaborator permission API returning `write`, `maintain` or `admin` | `author_association`: the bot reads `CONTRIBUTOR`. The permission API also rejects apps ("not a user") |
| Who pushed | GitHub's activity log for the branch, by numeric id | Commit authors are free text, and bot commits are unsigned, so signatures cannot be required |
| CI | The `report` check, which the ruleset pins to GitHub Actions (`15368`) | On its own: same-repo `pull_request` workflows run from the PR's branch, hence the path allowlist |

**The model can veto, never approve.** PR text is untrusted on a public repo, so a persuasive PR body must
not be able to earn its own merge. #1458's `change`, `risky`, `breaking` and `complex` come from a model reading that
text, so they are required but never sufficient. The deterministic rules (2, 3, 4 and the Codex thread count) are the
real gate, which is why path, size and pusher limits stay tight even once the classification is trusted.

**Codex is not a security control here.** Its prompt names security once and has no security checklist. `build-pr-context.sh`
tells it the PR text is data, not instructions, but it still judges the change against the PR's stated intent, so a framing
in that text can still shape a verdict. On the `explore` driver, `verify-explore.mjs` refuses to post a review whose
`observed_shortstat` differs from the real diff or which claims no changed file read, so an unread checkout does not print as
clean. That check proves the model saw the diff, not that it looked for security problems. Treat a clean review as necessary
evidence of quality only.

**Raw output sits at line 3 on deprecated drivers only.** When the review is not JSON, the action posts it straight after
the `sha=` stamp. `explore` output always passes through `verify-explore.mjs`, so this needs a hand-dispatched `claude` or
`codex` run, which takes write access. The reader has to refuse a line 3 marker if that path stays.

**Limits that remain.** Local agents run with Alan's `gh` login, so they can approve as him, and can reach this script
and its policy file. A push under that login fails rule 2. Before phase 4 the weekly report also has to flag any
`alantreadway` approval with no matching click or decision record, and the executor has to log the policy's git commit
and refuse a dirty checkout. A renamed file reports only its new path through `gh pr view`, so the executor reads
`/pulls/N/files` with `previous_filename` instead.

**Executor rules, extending job 14:**

- Re-read everything at execution time, bypassing the per-run PR cache. If the head differs from the card,
  refuse and refresh.
- Approve through `POST /repos/…/pulls/N/reviews` with `commit_id`, because `gh pr review` has no head pin.
- Approvals made by a policy use `alantreadway` with the fixed body `<!-- auto-approval -->`, sent in the same
  call as the approval, so the approval can never exist without the marker. It renders as a plain approval with
  no visible text. Approvals Alan clicks himself carry no marker. The body is a constant, never built from PR
  text or issue data.
- The marker is subtle, not secret: anyone reading the raw review or the API can see it on this public repo.
  Colleagues cannot see it in the normal view either, so **tell the team before auto-approval goes live**.
- Before phase 4, make `needsAnswer` and `isActionable` strip HTML comments before testing for an empty review
  body. Today a non-empty body on an approval counts as feedback to answer, so the marker could turn on
  `PR: Feedback` and wake an agent.
- Merge with `gh pr merge --match-head-commit <head>`. Never `--admin` outside ag-dev-prompts.
- Any failed read means no action and a logged reason.
- Limits: a daily cap on auto merges (start at 3), a kill switch file (`~/.ag-jira-sync/desk.pause`), and
  no auto merges while a release freeze is on.
- Public text stays clean: no `MAG-N`, agent or squad references in approval bodies or JIRA comments. The
  existing no-leak check covers JIRA and should cover these as well.

### 4. Log: decision records

Append-only rows in `ag_decision_record` (section 3), one per decision, human or policy. Automated ones also
post a `/note` on the issue, which starts no run. The self-host's existing pre-deploy `pg_dump` covers them.

```json
{
  "id": "01J…",
  "at": "2026-11-05T12:00:00Z",
  "issue": "MAG-964",
  "subject": "ag-grid/ag-charts#8960",
  "head": "bae8fb5da4",
  "card": "bot_pr",
  "policy": { "id": "aiw-tiny", "commit": "abc123", "level": "shadow" },
  "recommended": "one_tap_review",
  "actor": "alan",
  "action": "approve_merge",
  "override_reason": null,
  "evidence": { "loc": 12, "files": 2, "report": "pass", "codex_sha_matches": true,
                "labels": ["Breaking", "Small fix"] },
  "blocked_by": ["label: Breaking"],
  "outcome": "merged",
  "later": { "reverted": false, "regression_tickets": [] }
}
```

**High-level review:** an Auto tab on the Decisions page listing what ran and why, plus a daily count (auto,
held, cancelled, failed).

**Refinement:** a weekly autopilot, in the style of the existing reconciliation one, reads the log and files
one issue:

- Agreement rate per policy, and the most common override reasons. Overrides take one tap to explain: "too
  big", "wrong class", "needs product call".
- Outcomes joined back on: reverts, `CRT` regression tickets naming the PR, comments after merge. The decision
  log is the way to find these PRs. The marker is a cross-check through the reviews API; GitHub search is not
  confirmed to index it.
- Proposed threshold changes, as a diff to `policy.json`. **It never edits policy itself.**

Starter thresholds: propose promotion after 30 shadow decisions at 95% agreement or better with no reverts.
Any revert drops that policy straight back to `one_tap`.

## 3. The Decisions page in the fork

**Split of responsibilities.** The server stores and displays; it never touches GitHub or JIRA. Credentials
stay on the Mac, with `ag-jira-sync`.

```
ag-jira-sync poll (every 3 min)  -- upserts cards ------> Multica server (Postgres)
Decisions page                   -- creates intent -----> Multica server
                                 <- WS invalidate ------
Desk worker (new launchd job)    -- claims intent ------> Multica server
    |  re-reads state, then acts with gh / JIRA / multica
    '---------------------------- result + record ------> Multica server
```

Two design choices remove the fork's main weaknesses:

- **Actions take seconds, not a poll.** A small always-on worker (`ag-jira-sync.mjs --desk-worker`, its own
  launchd job) polls for pending intents every 5 s or so. The poll job keeps building cards; the worker only
  executes.
- **Cards are data, so the policy can change without a deploy.** The engine sends each card's text, chips,
  blockers and action list as JSON, and the page renders whatever it gets. New card types and actions ship
  from `ag-jira-sync`. Only a new kind of UI needs a web image rebuild.

### Nav and routes

`Decisions` goes in `personalNav`, directly after Inbox, with a count badge built like Inbox's (cards where
`waiting_on = alan`). The workspace has one member, so there is no per-person scoping to design.

| Change | Where |
|---|---|
| Nav entry, badge | `packages/views/layout/app-sidebar.tsx` |
| Path, icon, nav key | `packages/core/paths/paths.ts`, `route-icons.ts` (and their consistency tests) |
| Web route | `apps/web/app/[workspaceSlug]/(dashboard)/decisions/page.tsx` |
| Desktop route | `apps/desktop/src/renderer/src/routes.tsx` |
| Reserved slug | `server/internal/handler/reserved_slugs.json`, then `pnpm generate:reserved-slugs` |
| Page | `packages/views/decisions/` (new) |
| Client state | `packages/core/decisions/` (new; TanStack Query, keys include `wsId`) |
| API client, schemas | `packages/core/api/ag-client.ts`; zod schemas with `parseWithFallback` and malformed-response tests |
| Strings | The `ag` namespace in 6 locales, page chrome only. Card text comes from the engine in English |
| Docs | `ag-patches/PATCHES.md`, as the 25th patch |

Clicking an issue reference opens the existing issue peek, so the diff links, comments and history stay one
click away.

### Data model

Fork migrations from `9004`, no foreign keys, and each index in its own `CREATE INDEX CONCURRENTLY`
migration. Queries go in `server/pkg/db/queries/ag_decisions.sql`.

| Table | Holds | Written by |
|---|---|---|
| `ag_decision_card` | Current cards: `dedupe_key`, `issue_id`, `waiting_on`, `revision`, `head_sha`, `content` (JSON), `first_seen_at`, `resolved_at`, `snoozed_until` | Engine |
| `ag_decision_intent` | A requested action: `card_id`, `card_revision`, `action_id`, `actor_user_id`, `status`, `execute_after`, `expires_at`, `result` | Page creates; worker updates |
| `ag_decision_record` | The append-only log in section 2 | Worker and engine |

A card the engine no longer sees gets `resolved_at`, so it leaves the list by itself.

### API

All under `/api/ag/decisions`, workspace-scoped, behind `RequireHumanActor` so agent task tokens (`mat_`) get
a 403.

| Route | Purpose |
|---|---|
| `GET /` , `GET /count` | List cards; badge count |
| `POST /{id}/intents` | Request an action, with the `revision` the page was showing |
| `DELETE /intents/{id}` | Cancel, including during an `auto_hold` window |
| `PUT /cards` | Engine: batch upsert and resolve |
| `POST /intents/claim`, `POST /intents/{id}/result` | Worker: claim and report |
| `GET /records` | Auto tab and weekly report |

A new WebSocket event, `ag:decision_changed`, invalidates the Query caches. Phase 1 has the read routes, `PUT /cards`
and `POST /records`; the intent routes come with phase 2.

### Behaviour that matters

- **One-tap state:** the page shows `Queued`, then `Working`, then the result on the card. The card is removed
  only once the server says the action succeeded, and a failure stays visible with a retry.
- **Stale intents never run.** An intent expires if unclaimed after 15 min, so a sleeping Mac cannot merge
  something hours later. The worker refuses a `card_revision` or head that no longer matches.
- **Engine heartbeat:** the page shows "Last updated N min ago" and warns once the engine has been silent past
  about 10 min, because the cards may be out of date.
- **Trust boundary:** the server guard stops agents using their task tokens. It does not stop a local agent
  reading the CLI token from disk, but those agents already run with Alan's `gh` login, so extra protection
  here would add complexity without closing the gap.

The engine sends through `multica-ag decisions push`, so `ag-jira-sync` never reads the CLI's token. The CLI's `mul_`
token is not classed as a machine token, so it should pass `RequireHumanActor`; the first live push confirms that.

### Rebase cost

New code stays in `ag_*` and new directories. Upstream files take only the small hooks above, mostly one line
each. `app-sidebar.tsx`, `router.go` and `routes.tsx` are the likely conflict points.

## 4. Phases

| Phase | Delivers | Estimate | Check before moving on |
|---|---|---|---|
| 0. Clear noise | Done. Job 3 parks author's-turn items and titles escalations by AIW's ask. Closed CRT mirrors cancelled | Done | Measured below |
| 1. Read-only page | Tables, read API, nav and page, engine writes cards and shadow records | About 3 days | Alan agrees the list matches what is really stuck |
| 2. One-tap actions | Intents, desk worker, batches, override reasons | About 2 days | A week of real use |
| 3. Backtest | Replay `aiw-tiny` over the last 100 bot PRs, joined to reverts and regression tickets | About half a day | Lane size and miss rate look acceptable |
| 4. Auto lane | `auto_hold` merge, Auto tab, weekly refinement report | About 1 day, then 2+ weeks of shadow data | Promotion thresholds met |

**Phase 0 result.** The queue goes from 17 to 16, not the 13 estimated. Of the 4 items expected to park, 2 had gone
green by build time and MAG-958's `PR: Feedback` label came from the PR author's own comment, so only MAG-920 (a real
conflict) parks. The bigger gain is that 6 items now say what they need. Two lessons for the engine: read whose turn
it is with job 3's `prTurn`, not the `PR: Feedback` label, which over-reports on colleague PRs; and removing noise
will not shrink this queue much, so batching and one-tap plan approvals are what have to.

**What phase 1 leaves out.** Agent question cards show the title, not the agent's options (those arrive with the
decision block in phase 2). `aiw-tiny` cannot yet read the AIW `Auto-proceed` signal or a colleague's comment hold on the
ticket, so each shadow record lists them under `not_checked`, and the backtest has to apply them before any promotion.
Every bot PR also stays held until #1458 (merged to `canary`) reaches ag-charts and writes the classification line.
Issues with a run in flight are not told apart from stuck ones beyond their status.

Phase 1 ships through the normal path: `build-backend.sh`, `build-web.sh`, then `deploy-selfhost.sh`, which
takes the `pg_dump` first.

## 5. Decisions needed from Alan

None open.

Decided:

- **UI home:** a page in the Multica fork, under Inbox.
- **Approval identity:** `alantreadway`, with the hidden review marker described under the executor rules.
- **Closed CRT mirrors:** cancelled, and job 16 now does this itself (`ag-jira-sync` `e43116e`).

## 6. Risks

| Risk | Mitigation |
|---|---|
| Rubber-stamping: one-tap approves become unread approves | Evidence on the card; `Breaking` needs a second tap; agreement rate is tracked |
| Worker and sync poll act on the same issue | Worker re-reads and pins the head; every step is safe to repeat |
| Rebases get harder with a 25th patch | New code in `ag_*` files and new directories; upstream files take small hooks only |
| `Breaking` over-labels and starves the auto lane | Shadow log shows how often Alan overrides it; tune from data |
| Regression after an auto merge | Daily cap, hold window, automatic demotion on revert, regression tickets joined to the log |
| Comment formats drift (AIW, agents) | Unparseable means a plain card with a link, never a guessed button |
