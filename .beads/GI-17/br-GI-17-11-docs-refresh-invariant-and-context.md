# Bead br-GI-17-11: the amended invariant, and the doc refresh the searches bound

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Design decisions D7, §Risk areas R6

- **Priority**: P1 (high)
- **Dependencies**: br-GI-17-01, br-GI-17-02, br-GI-17-03, br-GI-17-04, br-GI-17-05, br-GI-17-06,
  br-GI-17-07, br-GI-17-08, br-GI-17-09, br-GI-17-10
- **Blocks**: none

## Description

`CLAUDE.md`, `README.md`, and the context docs still describe an API that is read-only with one
write route, talking to a SQLite file with exactly one writer. Both claims are now false, in ways
this ticket made false. This bead amends them — and it is deliberately the **last** bead, because
the wording it has to publish describes the finished state.

**The refresh is bounded by the D7 governing searches, not by a hand-listed sample.** The
sample-vs-class defect recurred three rounds running in this plan's review (F1.3 docs → F5.2 docs →
F6.1 **source comments**), so the list below names the class's *members*, not its *bounds*. The
bound is: run the searches, assert each positive control, and require the search's **claim-bearing**
hit set to be empty.

**1. `CLAUDE.md`, amended twice.**

*(a) The single-writer invariant* ([CLAUDE.md:89](../../CLAUDE.md#L89)). Name **both** purge writers
honestly:

- **In-process purge** (the scheduled/startup run and `POST /api/purge`) uses the store's writer
  connection, `SetMaxOpenConns(1)`, so it queues behind ingest rather than racing it.
- **`lens purge`** is a *separate process with its own writer connection*, serialized **cross-process**
  by WAL plus `busy_timeout(5000)` — the same protocol every other external opener relies on.

**Do not publish a "the purge path is serialized through the same single connection" claim.** It is
false for `lens purge`, and R6 exists precisely because a claim its own D9 contradicts is a
documented lie.

*(b) The one-way data-flow bullet.* CLAUDE.md states the flow is `client → proxy → (tee) → sink →
consumer → {parse, analyze} → store → api → web`. A purge runs `api → store` and reads no rows
first, which that bullet does not describe. Amend it to admit the purge's direction rather than
leaving a sentence the new code contradicts.

Also note the listener now carries **three** write routes (replay, prices, purge) — one of them
destructive — where CLAUDE.md's security posture rests on the API being read-only and loopback-bound.
The guard is unchanged (`replayOriginReject`, parameterized by action); the *count* is what changed.

**2. `README.md`.** Retention, the purge commands, and the Settings tab, for a user rather than a
maintainer: what `retention_days` does, that `0` means keep forever, that the unpriced purge is a
separate action, and that `lens purge --vacuum` is what actually returns disk.

**3. The two governing searches over the whole tree.** Frozen paths excluded
(`-g '!docs/planning/GI-*' -g '!docs/superpowers/specs' -g '!.beads/GI-1' -g '!.beads/GI-4'
-g '!.beads/GI-15'`). Each search's positive control must be demonstrated **before** an empty
claim-bearing result counts — `rg` is Rust regex, so `\|` is a literal pipe and silently matches
nothing, which is how a gate becomes decoration.

**Search 1 — single-writer.** Positive control: `internal/store/store.go:4`.

```
rg -n -i -g '!docs/planning/GI-*' -g '!docs/superpowers/specs' \
      -g '!.beads/GI-1' -g '!.beads/GI-4' -g '!.beads/GI-15' \
      -e 'exactly one goroutine' -e 'only the consumer goroutine' \
      -e 'ever calls a writer method' \
      -e 'consumer (stays|remains) the (only|sole) writer' \
      -e "the consumer's single writer" -e 'sqlite has (exactly )?one writer' \
      -e 'must not become a second writer'
```

**Search 2 — the read-only API / the one write route.** Positive control:
`internal/api/broker.go:1`.

```
rg -n -i -g '!docs/planning/GI-*' -g '!docs/superpowers/specs' \
      -g '!.beads/GI-1' -g '!.beads/GI-4' -g '!.beads/GI-15' \
      -e 'read-only (json )?api' \
      -e '(the|our) (api|dashboard|json api) is read-only' \
      -e 'the one (credentialless )?write (route|guard)' \
      -e 'the one state-changing route' \
      -e 'the only .{0,12}(write|state-changing) route'
```

Verified hits these searches find today, for illustration — **not** the bound:

| Hit | What it claims |
|---|---|
| [architecture.md:48](../../docs/context/architecture.md#L48) | "read-only JSON API + SSE broker + the one write route" |
| [api-surface.md:70](../../docs/context/api-surface.md#L70) | "Replay's guard (the one write route)" |
| [security-and-permissions.md:44](../../docs/context/security-and-permissions.md#L44) | "the one credentialless write guard in the system" |
| [data-model.md:105-107](../../docs/context/data-model.md#L105-L107) | "Only the consumer goroutine ever calls a writer method" |
| [CLAUDE.md:89](../../CLAUDE.md#L89) | "SQLite has exactly one writer: the consumer goroutine" |

Also in the class per the plan's R6: `architecture.md` `:54`,
`security-and-permissions.md` `:33-35`/`:39`/`:64`, `data-model.md` `:103-107`,
`api-surface.md` `:28`/`:95`, and `docs/context/INDEX.md`'s conditional table plus its
"Last generated / refreshed" stamp — the stamp is part of the deliverable, since a refresh that does
not restamp the index is indistinguishable from a stale one.

**The trap: several of those lines are not hits of either search, so the gate cannot see them.**
This is not hypothetical — it is the same defect the plan already documents for `api.go`'s
`:78`/`:103`/`:335` (the claim wraps across source lines, so line-based `rg` misses it) and fixes by
naming those lines **explicitly** in the Modify row. The same treatment is required here. Verified
by running search 2 exactly as written — it returns 7 hits, and none of these three is among them:

| Line | What it says | Why the search misses it |
|---|---|---|
| [api-surface.md:27-28](../../docs/context/api-surface.md#L27-L28) | "All routes except replay are **read-only** and rely on loopback binding for their 'no auth needed' rationale" | The pattern is `read-only (json )?api`; "are read-only" does not match |
| [architecture.md:55-56](../../docs/context/architecture.md#L55-L56) | "The one / state-changing route, replay, adds its own Origin/Host allowlist" | The claim wraps across two source lines |
| [security-and-permissions.md:64](../../docs/context/security-and-permissions.md#L64) | "plus the one application-layer guard above" | "the one application-layer guard" is not "the one write route/guard" |

All three are falsified by this ticket — the first two the moment `POST /api/prices` and
`POST /api/purge` exist. Because the bead's own Outcome Definition makes "each search's
claim-bearing hit set is empty" the deliverable and its Files to Touch the searches' hit set, a naive
reading of this bead would let it be marked done with those three lines still asserting a read-only
API with exactly one state-changing route.

So each of them is a **named line edit**, and Test Specification step 4 gains a companion: for each
falsified claim the searches cannot see, run an assertion-form pattern with **its own positive
control** — e.g. `rg -n 'state-changing route'` (positive control `architecture.md:55-56` before the
change) and `rg -n 'except replay are'` (positive control `api-surface.md:28`). Without the named
lines and their own controls, an empty search result proves nothing about them.

**4. What this bead does *not* own.**

- **The subcommand-inventory search** is br-GI-17-08's: it registers the twelfth subcommand, so
  `docs/context/architecture.md:50` ("Eleven subcommand implementations") and
  `docs/context/cli-and-tooling.md:6` ("All eleven names are implemented") are amended there.
- **The in-source read-only hits** — `internal/api/api.go` `:67`/`:78`/`:103`/`:335`,
  `internal/api/broker.go`'s package doc, `internal/api/api_test.go:83`,
  `internal/cli/replay.go:207` — are br-GI-17-04's, per D7's ownership note.
- **The in-source single-writer hits** — `internal/store/store.go`'s package doc and
  `internal/api/api.go:455-456` — are br-GI-17-05's.
- **Frozen paths stay.** Other tickets' plans, merged specs, and prior tickets' beads are historical
  records of what was true for *those* tickets and must not be rewritten to satisfy a search. E.g.
  `docs/planning/GI-15-pagination.md:155` states the single-writer invariant and **stays**.
- **Declared true lines stay**, named so they read as deliberate rather than as misses: `store.go:75`,
  `consumer.go:94`, `consumer_test.go:498`/`:546`, `architecture.md:46`, `testing-and-quality.md:72`,
  `api.go:326`/`:354`, and the `internal/consumer/analyzer.go`/`session.go` "read-only" comments.

## Rationale

This is the bead that makes the repo's documentation true again, and it is last because it describes
the finished state — including the write-route count and the two purge writers, neither of which is
knowable until the earlier beads land. The searches exist because the alternative has already been
tried three times in this plan's own review history and failed three times: a hand-listed sample
misses the member nobody thought of, and the miss is invisible precisely because the list looked
complete.

## Outcome Definition

- `CLAUDE.md`'s single-writer invariant names both purge writers and their two different
  serialization mechanisms, and contains no "same single connection" claim for `lens purge`.
- `CLAUDE.md`'s one-way data-flow bullet admits the purge's `api → store` direction.
- The write-route count in `CLAUDE.md` matches the routes actually registered in `internal/api`.
- `README.md` documents `retention_days`, the two purge actions, and `lens purge --vacuum`.
- Both searches run with their frozen-path exclusions, each positive control demonstrated as a hit,
  and each search's **claim-bearing** hit set is **empty**.
- `docs/context/INDEX.md`'s "Last generated / refreshed" stamp is updated.
- The topic-word hit set is **not** required to be empty — the declared true lines and the frozen
  records are expected to remain, and they are named above so they are not mistaken for misses.

## Test Specifications

Documentation, so the verification is the searches themselves, and it must be shown rather than
asserted:

1. **Before** editing, run both searches and record the hits. This is the positive control: a search
   that returns nothing *before* the change is a broken search, not a converged tree.
2. Confirm each pattern contains `|`, never `\|`.
3. Edit.
4. Re-run both searches. Classify every remaining hit as **claim-bearing** (must be empty), a
   **declared true line**, a **frozen path**, or **the pattern quoting itself**. Any claim-bearing
   hit that survives is this bead's to fix or to escalate — not to reclassify as a true line to make
   the set empty.

   On that fourth category: `rg` skips hidden files and directories by default, so `.beads/` is not
   searched at all and the `-g '!.beads/GI-*'` exclusions are belt-and-braces rather than the thing
   doing the work. Under `--hidden` the searches *would* match this plan's and these beads' own
   pattern literals — a bead that quotes the pattern it is gated by is not a claim about the
   codebase program. Such a hit stays, and it is neither a claim-bearing hit nor a declared true
   line; saying so now is what keeps step 4 from stalling on one.
4b. **Then run the named-line controls.** The searches cannot see `api-surface.md:27-28`,
   `architecture.md:55-56`, or `security-and-permissions.md:64` (see the table above), so an empty
   search result says nothing about them. For each, run an assertion-form pattern with its own
   positive control (`rg -n 'state-changing route'` against `architecture.md:55-56`;
   `rg -n 'except replay are'` against `api-surface.md:28`), demonstrated as a hit *before* the edit,
   and require the claim-bearing set empty after. Reading the three lines is not a substitute — a
   line that is not in a gate's result set is exactly the one that gets left behind.
5. Grep `docs/context/INDEX.md` for the refresh stamp and assert it is today's date.
6. Cross-check the write-route count in `CLAUDE.md` against the route registrations in
   `internal/api` — the prose against the source of truth, not against itself.

## Files to Touch

- `CLAUDE.md` (modify — the single-writer invariant, the one-way data-flow bullet, the write-route
  count)
- `README.md` (modify — retention, purge, the Settings tab)
- `docs/context/architecture.md` (modify — the claim-bearing hits the searches find, **plus the
  named line `:55-56`**, which no search sees)
- `docs/context/api-surface.md` (modify — as above, **plus the named line `:27-28`**)
- `docs/context/security-and-permissions.md` (modify — as above, **plus the named line `:64`**)
- `docs/context/data-model.md`, `docs/context/testing-and-quality.md` (modify — the claim-bearing
  hits the searches find)
- `docs/context/INDEX.md` (modify — the conditional table and the "Last generated / refreshed" stamp)

---

## Review Notes

**The gate could not see three of the lines the bead itself named as in the class.** This was the
most consequential finding in the set, because the bead's Outcome Definition makes "each search's
claim-bearing hit set is empty" the *deliverable* — so a gate blind to three falsified sentences
would have let the bead be marked done with them still asserting a read-only API with one
state-changing route:

| Line | Why the search misses it |
|---|---|
| `api-surface.md:27-28` "All routes except replay are read-only" | the pattern is `read-only (json )?api`; "are read-only" does not match |
| `architecture.md:55-56` "The one / state-changing route, replay" | the claim wraps across two source lines |
| `security-and-permissions.md:64` "the one application-layer guard above" | not "the one write route/guard" |

All three are now **named line edits** with their own assertion-form controls (step 4b), verified by
running search 2 as written: it returns 7 hits and none of these is among them. This is the same
mechanism the plan already documents for `api.go`'s `:78`/`:103`/`:335`, applied where it had been
missed. The lesson is general — a gate's empty result is evidence only about the lines the gate can
match, and the lines it cannot match are exactly the ones nobody re-reads.

**Two smaller corrections.** The searches' `-g '!.beads/GI-*'` exclusions are no-ops in practice
(`rg` skips hidden directories), which is fine but needed stating so step 4's classification has a
category for a hit inside `.beads/GI-17/` — the pattern quoting itself, which neither stays nor is
claim-bearing. And `Dependencies` now enumerates the ten beads rather than using a range, matching
`br-GI-15-08`'s form.

