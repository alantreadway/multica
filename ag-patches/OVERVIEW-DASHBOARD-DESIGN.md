# Overview dashboard: design

Status: built as option B, patch `feat(ag-patches): Overview dashboard` (see the
[PATCHES.md section](./PATCHES.md#overview-dashboard-backend--web)). Not deployed yet. Numbers in section 1 come from
read-only queries on the self-host database on 2026-11-06 (a Friday), about 15:20.

## What was built, and how it differs from the options below

Option B: one endpoint, `GET /api/ag/overview`, computes the page in SQL and Go from data the server holds. Task,
issue and Decision Desk WebSocket events refetch it, at most once every 5 seconds. No engine work. Alan's decisions
changed it from option B as written in four ways:

1. **The ETA ships in the first cut**, using the per-unit stage method from section 2, as a p50-p85 range. It is
   hidden when any open unit's state has fewer than 20 finished samples, and it is labelled as covering known units
   only, never as a delivery date.
2. **Every tree is split into known and unknown scope.** Known: units done of known, a stage strip per open unit, and
   the ETA. Unknown: plain facts that suggest more work is coming (open issues not yet broken into units, units added
   in the last 3 days, how much each finished wave grew after day one), and "Scope still open" when either of the
   first two is non-zero, else "Scope looks complete". No number is invented for unknown work.
3. **Trees are pinned, not found automatically.** Alan pins a JIRA key or a root issue (MAG-n) on the page and unpins
   it there. Pins live in `ag_overview_pin`.
4. **A forecast log**, `ag_overview_forecast`: one row per pinned tree per day (p50, p85, known and done units, scope
   open), written by the first `GET` of the day, so the ETA can be scored later without a scheduler.

The `Overview` nav item sits above Inbox on web and desktop. It is not the landing page.

**Goal:** one main screen that shows what is happening now (agents running, what each works on, what is queued,
what waits on Alan) and how far bigger pieces of work have got, with an ETA where one can be trusted.

A bigger piece of work is a tree of Multica issues, for example everything under JIRA epic AG-9003 (chart shadow
and glow).

## 1. What the live data says

### AG-9003 is four trees, not one

Multica work on AG-9003 has run in four waves over five weeks, each with its own root issue:

| Root | Started | Wave | Issues under it | Closed | Took |
|---|---|---|---|---|---|
| MAG-22 | Oct 1 | Research, plan, U1–U4 units, fix-ups | 68 | 68 | 11 days |
| MAG-688 | Oct 23 | QA feedback: shared shadow pass, glow | 24 | 24 | 6 days |
| MAG-921 | Nov 4 | Redo work held back from 14.3.0 (also holds AG-19162 work) | 5 | 5 | 2 days |
| MAG-950 | Nov 4 | Remaining shadow and glow gaps | 8 | 1 | open |

Four more issues name AG-9003 but sit outside the trees (MAG-40 review queue item, MAG-83, MAG-124, MAG-327).

Three things follow:

1. **`jira_key` alone misses work.** 64 issues carry AG-9003 in their metadata or title, but the trees hold 105
   issues. Wave 1's review and fix-up sub-issues have no `jira_key`.
2. **Parent trees alone mix work.** MAG-921's tree also holds AG-19162 (MAG-928, MAG-929).
3. **The epic outlives any one tree.** New roots appear when QA or a release cut sends work back.

So a "work tree" should be: every issue whose metadata names the key (`jira_key`, `review_jira`, `source_key`),
plus their descendants, minus descendants that name a different key. Each root becomes one wave.

### Scope grows after day one

| Wave | Issues at end of day 1 | Issues at the end | Growth | Count-based progress at end of day 1 |
|---|---|---|---|---|
| MAG-22 | 33 (32 done) | 69 | 2.1x | 97%, with 10 days still to go |
| MAG-688 | 8 (2 done) | 25 | 3.1x | 25%, and it stayed under 50% for 3 days |

A progress bar built from issue counts would have read 97% on day one of an eleven-day wave.

### Rework is the norm

- Wave 1 created 29 of its 68 issues as reviews, re-reviews or fix-ups. Later waves track rounds in the
  `review_round` metadata instead of new issues.
- "CHANGES NEEDED" appears in 33, 20, 6 and 8 comments across the four waves.
- Across all 252 units with a `review_round`: 21% pass at round 1, the median is round 2, p90 is round 5, the
  maximum is 11. MAG-978 (current wave) is at round 6.

### Waiting dominates, not agent time

Units (issues with `unit_flow`) that reached `done`:

| Review rounds | Units | Created to done, p50 | p90 | Agent run time, p50 | p90 |
|---|---|---|---|---|---|
| 0–1 | 50 | 1.2 h | 27 h | 0.3 h | 1.9 h |
| 2–3 | 135 | 13.8 h | 60 h | 1.4 h | 4.9 h |
| 4+ | 56 | 23.1 h | 93 h | 2.6 h | 10.2 h |

- Agents run for about a tenth of a unit's elapsed time. The rest is review, CI, merges, nights and weekends.
- Last 30 days: created to first `in_review` is 2.2 h p50, 28 h p90; first `in_review` to `done` is 3.2 h p50,
  29 h p90.
- Wave 2's 8 PRs took 1.8 h to 44 h from open to merged, median about 19 h.
- Queue wait is not the limit: p50 0 min in every wave, p90 22 min in the current one.
- AG-9003 used about 930 runs and 162 agent run-hours across the four trees.
- Throughput across all work is 3 to 9 units a weekday, once 24, and almost none at weekends.

### Signals that are not there

- **Acceptance criteria:** 0 of 1,021 issues use `acceptance_criteria`. AC-based progress is out.
- **PR size:** the PR snapshot rows for AG-9003 have `additions`, `deletions` and `changed_files` at 0. Weighting
  by size needs the engine's `gh` reads, or a backend change to fill those columns.
- **Stage:** `unit_flow` (for example `bench,tests,review`) is on 260 issues, `review_round` on 260, the `stage`
  column on 157. These are usable.

### Right now

| What | Count | Detail |
|---|---|---|
| Runs running | 5 | Builder on MAG-978, Benchmarker on MAG-979, three on other work |
| Runs queued | 3 | |
| Desk cards waiting on Alan | 1 | MAG-797 (blocked). It was 2 ten minutes earlier |
| Other Desk cards | 31 | Agent 12, colleague 7, nobody 12 |
| MAG-950 tree | 1 of 8 done | See below |

MAG-950's units, with who each waits on according to the Desk cards:

| Unit | State | In this state | Waits on |
|---|---|---|---|
| MAG-973 options API spike | `handoff`, PR #8973 draft, review required | 26.5 h | nobody |
| MAG-977 second cast slot | `in_review`, round 2, #8984 | 20.8 h | colleague |
| MAG-978 series-level glow | `in_progress`, round 6, Builder running | 1.0 h | agent |
| MAG-979 highlightedItem shadow | `in_progress`, round 0, Benchmarker running | 2.6 h | agent |
| MAG-982 tidy up leftovers | `in_review`, round 1, #8978, CI red | 0.8 h | colleague |
| MAG-980, MAG-981 | `backlog` | | |

For scale, the p90 time a unit spends in one state (last 60 days) is 5 h in `in_progress`, 9 h in `handoff`,
15 h in `blocked` and 21 h in `in_review`. So MAG-973 is well past normal and nobody holds it, and MAG-977 is at
the edge. Neither shows up anywhere today unless Alan opens the issue.

## 2. Ideas shared by every option

### Progress

Count **units**, not issues. A unit is a leaf issue that owns a PR or has `unit_flow`. Research, review and
umbrella issues show in the tree but stay out of the denominator. A unit is done when its PRs are merged (job 10
closes it then, so this matches `done`).

Each unit gets a stage strip instead of a percentage:

```
build ─ bench ─ tests ─ review r2 ─ merge
  ✓       ✓       ✓        ●
```

The tree shows `units done / units known`, a count by stage, and for each unit who it waits on (joined from the
Desk card for that issue). No weighting by size or type in the first cut,
because size is not stored and nothing shows that type predicts rounds.

### "Now"

- **Running:** agent, issue, run time so far, review round, tree it belongs to.
- **Queued:** agent, issue, waiting for how long.
- **Waiting on Alan:** Decision Desk cards with `waiting_on = alan`, with a link to the card. The Overview never
  acts; the Desk does.
- **Ageing:** units that have sat in one state past that state's p90 (today 5 h `in_progress`, 9 h `handoff`,
  15 h `blocked`, 21 h `in_review`), with who they wait on.
- **Elsewhere:** counts of cards waiting on agents, colleagues and nobody.

The sidebar keeps the token rate and run totals. The page lists the runs behind those totals and does not
repeat the totals.

### ETA: three ways, judged on AG-9003

| Method | How | Verdict |
|---|---|---|
| Count burn-down | Open issues ÷ recent close rate | Rejected. Read 97% on day 1 of an 11-day wave |
| Per-unit stage percentiles | For each open unit, draw "time from this state to done" from units done in the last 60 days. The tree's remaining time is the latest unit. Repeat 1,000 times, report p50 and p85. Skip weekends | Usable for **known units only**, with a wide range |
| Ageing only | No date. Flag units past their state's p90 | Most reliable, least satisfying |

How good would the stage method be? Per-unit p90 is 4 to 5 times p50, so the range is wide by nature. For MAG-950
it would read roughly "known work: Mon 9 Nov (p50) to Wed 11 Nov (p85)". It cannot see units that do not exist
yet, and both finished waves grew 2 to 3 times after day one. It also says nothing about a new wave. Treat it as
"earliest plausible finish of what we know about", not a delivery date. Label it that way on screen.

It has not been backtested: the history of `review_round` is not stored, only its current value. A daily
forecast log (section 5, decision 4) would let us score it after two or three weeks.

## 3. Options

### Option A: Live board, client only

Built from queries the app already has. No new API, table or engine work.

```
┌ Overview ───────────────────────────────────────────────────────────────┐
│ NOW                                                                     │
│  Running (5)                          Waiting on you (1)   → Decisions  │
│  ● Builder      MAG-978 glow cascade   r6   18m         MAG-797 blocked │
│  ● Benchmarker  MAG-979 hover fix      r0    7m                         │
│  ● Reviewer     MAG-1000 allowlist          15m                         │
│  Queued (3)  Lead MAG-1001 · Coordinator MAG-1020 · Reviewer MAG-1011   │
├─────────────────────────────────────────────────────────────────────────┤
│ TRACKED WORK                                       [+ track key/issue]  │
│  AG-9003 · MAG-950   units 1/7   build 2 · review 2 · handoff 1 · new 2 │
│   977  review r2      21h  colleague    982  review r1  CI red  colleague│
│   978  build r6        1h  agent        973  handoff  27h ⚠     nobody   │
└─────────────────────────────────────────────────────────────────────────┘
```

- **Now:** `agentTaskSnapshotOptions` (active tasks with `issue_id`), the agent list, and `/api/ag/decisions`
  for cards. All already kept fresh over WebSocket.
- **Work tree:** Alan tracks a JIRA key or a root issue. The list is kept in a small Zustand store in
  `packages/core` with `StorageAdapter`. The page lists issues with `metadata={"jira_key":K}`, then children per
  root.
- **Progress:** units done / known, stage strip from `status`, `unit_flow`, `review_round`.
- **ETA:** none. Ageing uses fixed thresholds in code (today's p90s: 5 h, 9 h, 15 h, 21 h), since the client
  has no history, and they go stale.
- **Size:** about 12 h (page 5, store and hooks 2, routes, nav, slug and 6 locales 2, tests 3).
- **First cut leaves out:** waves, ageing.
- **Weak points:** one request per root for the children, and a wave-1-sized tree needs several. Thresholds go
  stale. Cards come from the engine, so "waiting on Alan" is up to 3 min old.

### Option B: Server-side Overview API (recommended)

One Go endpoint computes everything in SQL from tables the server already has.

```
┌ Overview ───────────────────────────────── live · Desk cards 1 min ago ┐
│ NOW                                                                    │
│ ┌ Running 5 ───────────────┐ ┌ Waiting on you 1 ─┐ ┌ Ageing 2 ────────┐│
│ │ Builder   MAG-978 r6 18m │ │ blocked  MAG-797  │ │ MAG-973 handoff  ││
│ │ Bench.    MAG-979     7m │ │ → Decisions       │ │  27h · nobody    ││
│ │ + 3 on other work        │ │                   │ │ MAG-977 review   ││
│ │ Queued 3 · oldest 14m    │ │ colleague 7 ·     │ │  21h · colleague ││
│ └──────────────────────────┘ │ agent 12 · none 12│ └──────────────────┘│
│                              └───────────────────┘                     │
│ WORK                                                                   │
│ AG-9003  Chart shadow and glow        wave 4 · 5 weeks · 162 run-hours │
│   MAG-950  1/7 units  ██░░░░░░░░░  known work: Mon 9 – Wed 11 Nov      │
│     977 Second cast slot      ✓build ✓bench ✓tests ● review r2  colleague│
│     982 Tidy up leftovers     ✓build ✓tests ● review r1 CI red  colleague│
│     973 Options API spike     ● handoff 27h ⚠                   nobody  │
│     978 Series-level glow     ● build r6                        agent   │
│     979 highlightedItem shadow● build                           agent   │
│     980, 981                  backlog                                  │
│   MAG-22, MAG-688, MAG-921    waves 1–3 done ▸                         │
│ AG-19137  …                   4 open issues                            │
└────────────────────────────────────────────────────────────────────────┘
```

Mock: [`overview-mocks/option-b.html`](./overview-mocks/option-b.html).

- **Now:** active tasks joined to agent, issue and tree key; queued tasks with wait time; Desk cards by
  `waiting_on`; ageing units.
- **Work trees:** listed automatically: every key with an open unit (6 keys today). The tree rule from section 1
  runs as one recursive CTE. Done waves fold into one line.
- **Progress:** units done / known, stage strip, review round, CI and review state from `github_pull_request`.
- **ETA:** the per-unit stage method from section 2, computed in Go from a 60-day history query and cached for
  5 min. Shown as a range with "known work" in the label. Not shown when a tree has under 3 open units with
  history, or when a state has under 20 past samples.
- **Data:** no new tables in the first cut.
  - `GET /api/ag/overview` (workspace-scoped, `RequireHumanActor`), queries in
    `server/pkg/db/queries/ag_overview.sql`.
  - zod schema with `parseWithFallback` and malformed-response tests in `packages/core/api/schema.ts`.
  - Refresh: invalidate on the existing task and issue WebSocket events, debounced to once per 5 s. No new event.
- **Engine work:** none. The Desk cards it already writes give "waiting on Alan".
- **Size:** about 26 h. Go handler and SQL 7, ETA sampler 4, Go DB tests 5, core schema and query 2, page 6,
  routes, nav, reserved slug and 6 locales 2.
- **First cut (about 18 h) leaves out:** the ETA (ageing only), the folded wave history, run-hours.

### Option C: Engine-pushed snapshots

`ag-jira-sync` gets a job 18 that builds one JSON snapshot per JIRA key every 3 minutes and pushes it the way
job 17 pushes cards. The page renders whatever arrives.

```
┌ Overview ────────────────────────────────────── engine 2 min ago ┐
│ NOW  (from the last snapshot)                                    │
│  Running 5 · Queued 3 · Waiting on you 2                         │
├──────────────────────────────────────────────────────────────────┤
│ AG-9003  Chart shadow and glow · JIRA: In Progress · fix 14.4.0  │
│  units 1/7 · JIRA sub-tasks 3 not yet in Multica                 │
│  burn-up (14 days)   ▁▂▂▃▅▆▆▆▆▇▇▇█  known ━━  done ━━            │
│  PR #8996  +412 −88  Codex: 2 open threads  CI ✓                 │
│  known work: Mon 9 – Wed 11 Nov                                  │
└──────────────────────────────────────────────────────────────────┘
```

- **Now:** from the snapshot, so up to 3 min old. Could instead read the live task snapshot like option A.
- **Work trees:** the same rule, plus JIRA facts only the engine has: epic status, fixVersion, sub-tasks not yet
  mirrored into Multica (which shows some scope before it reaches Multica), and PR size and Codex state from `gh`.
- **Progress:** units, optionally weighted by changed lines.
- **ETA:** same stage method, plus a burn-up chart from stored snapshots, which also gives the history to score
  forecasts.
- **Data:** `ag_overview_snapshot` (`jira_key`, `taken_at`, `content` JSON), one index migration;
  `PUT /api/ag/overview/snapshots`; `multica overview push`; `GET /api/ag/overview`.
- **Size:** about 32 h. Job 18 10, table, API and CLI 8, page 8, tests 6.
- **First cut leaves out:** the burn-up chart and size weighting.
- **Weak points:** the Mac has to be awake. Fresh "now" data needs a second source anyway. The engine is already
  5,566 lines.

## 4. Recommendation

**Option B, in two steps, and borrow one piece of C later.**

1. **Step 1, about 18 h:** the Overview API and page with Now, work trees, units and stage strips, and ageing. No
   ETA. This answers "what is happening" and "what is stuck" from data the server already holds, live, with no
   engine or table work.
2. **Step 2, about 8 h:** add the stage-based ETA range. Add a small `ag_overview_forecast` table (key, day,
   p50, p85, known units) written once a day by the endpoint, so after two or three weeks we can say how often
   the p85 held.
3. **Later, if wanted:** the engine pushes only what the server cannot see (JIRA epic status, unmirrored
   sub-tasks, PR size) into a narrow table, and option B's endpoint joins it in. That is option C's value without
   moving "now" off the live data.

Why B over A: one request instead of one per root, real thresholds from history, and the tree rule lives in SQL
where it can be tested. Why B over C: "now" stays live, the Mac being asleep does not blank the page, and the
engine does not grow again.

Rebase cost matches the Desk: new code in `ag_*` files and new directories, one-line hooks in `router.go`,
`app-sidebar.tsx`, `routes.tsx`, `paths.ts` and `reserved_slugs.json`.

## 5. Decisions needed from Alan

1. **Where it is computed:** server SQL (B), engine snapshots (C), or client only (A). Recommended: B.
2. **ETA in the first cut:** ship ageing only and add the ETA range in step 2, or ship the range straight away
   with its "known work" label. Recommended: ageing first.
3. **What a work tree is:** every JIRA key with an open unit, listed automatically, or only keys or root issues
   Alan pins. Recommended: automatic, with pinning later if the list gets long.
4. **Forecast log:** keep a daily row per key (`ag_overview_forecast`) so the ETA can be scored, or skip it and
   judge the ETA by eye. Recommended: keep it; it is one table and one insert.
5. **Place in the app:** an `Overview` nav item above Inbox that becomes the workspace landing page, or a nav
   item only. Recommended: nav item above Inbox; decide on landing after a week of use.
