# Bead br-GI-17-11: the amended invariant, and the doc refresh the searches bound

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Design decisions D7, §Risk areas R6

- **Priority**: P1 (high)
- **Dependencies**: br-GI-17-01 … br-GI-17-10
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
   **declared true line**, or a **frozen path**. Any claim-bearing hit that survives is this bead's
   to fix or to escalate — not to reclassify as a true line to make the set empty.
5. Grep `docs/context/INDEX.md` for the refresh stamp and assert it is today's date.
6. Cross-check the write-route count in `CLAUDE.md` against the route registrations in
   `internal/api` — the prose against the source of truth, not against itself.

## Files to Touch

- `CLAUDE.md` (modify — the single-writer invariant, the one-way data-flow bullet, the write-route
  count)
- `README.md` (modify — retention, purge, the Settings tab)
- `docs/context/architecture.md`, `docs/context/api-surface.md`,
  `docs/context/security-and-permissions.md`, `docs/context/data-model.md`,
  `docs/context/testing-and-quality.md`, `docs/context/INDEX.md` (modify — the claim-bearing hits
  the two searches find, plus the refresh stamp)
