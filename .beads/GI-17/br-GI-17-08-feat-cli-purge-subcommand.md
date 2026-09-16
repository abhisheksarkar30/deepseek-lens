# Bead br-GI-17-08: the `lens purge` subcommand, and the twelfth name in the dispatch map

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Design decisions D7, D9, §Risk areas R2

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-17-05, br-GI-17-06
- **Blocks**: none

## Description

The one-shot purge, for the case the scheduled purge cannot serve: reclaiming disk when the proxy is
not running.

**Implement all three guards before the destructive path, and write their tests first.** Every
irreversible action in this ticket is reachable from this file, and the guards are what stand between
a typo and a deleted capture file.

**1. `lens purge` opens the store directly.** `lens replay` deliberately goes through the running
server's route to avoid a second writer; purge takes the opposite path on purpose, because the usual
reason to purge is to reclaim disk and that is exactly when you would rather the proxy were not
running. WAL plus `busy_timeout(5000)` serializes the two processes safely — this process is
serialized **cross-process**, not by the in-process `SetMaxOpenConns(1)` (D7).

It cannot use `openStore`: that helper calls `config.Load(nil)` and returns only the `*store.Store`
([format.go:220-226](../../internal/cli/format.go#L220-L226)), throwing away the resolved config, and
`lens purge` needs the configured `retention_days` as `--older-than`'s default.

**2. `config.Load(nil)`, then its own `FlagSet`.** `--older-than`, `--unpriced`, `--dry-run`,
`--yes`, `--vacuum` are **not** `lens` config flags, so they must never be passed to `config.Load`.
`applyFlags` registers only the config flag set with `flag.ContinueOnError`
([config.go:214-231](../../internal/config/config.go#L214-L231)) and `Load` returns its parse error
([config.go:259-261](../../internal/config/config.go#L259-L261)), so `config.Load(args)` on
`--unpriced` aborts with *flag provided but not defined* before the store ever opens. The house
convention for a subcommand with non-config flags is exactly what `Replay` does
([replay.go:38](../../internal/cli/replay.go#L38),
[replay.go:56-72](../../internal/cli/replay.go#L56-L72)): `config.Load(nil)`, then parse in its own
`flag.FlagSet`, then `store.Open(cfg.DBPath)`.

Note that `lens purge` does **not** call `cfg.Validate()`. That is why the negative-`retention_days`
case is handled by the guard below rather than by config validation — br-GI-17-06 added the
`Validate` case for `serve` and `doctor`, and this bead adds its own for the CLI path.

**3. The default must not be a delete-everything.**

- Explicit `--older-than <days>` requires `days >= 1`, refused with the message the route uses for
  its non-positive `days` (D9) — a `days <= 0` (zero **or** negative) is refused. `--older-than 0`
  would make the predicate `started_at < now`, which matches **every** row; a negative `days` is
  worse — `cutoff = now + |days|` also matches every row.
- When **neither** predicate flag is given, `--older-than` is the default using the configured
  `retention_days`, applied **only when `retention_days >= 1`**. When `retention_days <= 0` the
  command refuses and points the user at `--older-than <days>` or `--unpriced`. This is the guard
  that matters most: `retention_days` defaults to `0`, so without it a bare `lens purge` is a
  whole-table delete.
- **`--older-than` and `--unpriced` are mutually exclusive.** Passing both exits non-zero with a
  message naming both flags and stating that they select different predicates — e.g. "`--older-than`
  and `--unpriced` select different rows and cannot be combined". The two predicates are never OR-ed
  into one WHERE clause and never run as two sequential purges.

**Every guard executes before `store.Open`**, so a refused invocation cannot have deleted anything —
not "deleted nothing because the predicate was empty", but "never reached the database".

**4. One threshold everywhere.** `retention_days <= 0` means "retention not configured" — the
route's `days <= 0` guard, R2's requirement, and this CLI guard read the same bound, never a mix of
`== 0` / `<= 0` / `> 0`.

**5. `--dry-run`, `--yes`, `--vacuum`.**

- `--dry-run` still applies the same validation before it previews, and **skips `--vacuum`
  entirely**: it reports what a purge *would* delete.
- `--yes` gates the write, not the guard.
- `--vacuum` runs `(*Store).Vacuum(ctx)` after the purge. `VACUUM` needs free space on the order of
  the DB size and takes an exclusive lock that blocks ingest, so it is opt-in and never automatic
  (R3). The command reports the file size before and after, since the whole point of `--vacuum` is
  that the `.db` does not shrink after `DELETE`.

**6. Register `purge` and amend the dispatch-map prelude.** `cmd/lens/main.go`'s map gains its
twelfth entry and its prelude loses "Every name here is implemented: …which was the last stub" —
reword it for twelve, not thirteen. This amends the *code*, and the counts it implies are in the
docs; both are this bead's, via the search below.

**7. Amend `replay.go`'s and `replay_test.go`'s writer claims.** `replay.go`'s prelude says `lens
replay` does not open the store "so the consumer stays the only writer"
([replay.go:30-36](../../internal/cli/replay.go#L30-L36)); `lens purge` now does open one, so the
prelude must say *replay* does not, without implying no subcommand does. `replay_test.go`'s "must not
become a second writer" comments need the same qualification.

**8. Own the D7 subcommand-inventory search** (`docs/planning/GI-17-pricing-retention-purge.md` §D7
governing searches, search 3) — this bead registers the twelfth subcommand, so it is the bead whose
change falsifies the count:

```
rg -n -i -g '!docs/planning/GI-*' -g '!docs/superpowers/specs' \
      -g '!.beads/GI-1' -g '!.beads/GI-4' -g '!.beads/GI-15' \
      -e 'eleven (sub)?commands?' -e 'all eleven names' -e 'every name here is implemented'
```

- **Positive controls, asserted before the empty result counts:**
  `docs/context/architecture.md:50` ("Eleven subcommand implementations") and
  `cmd/lens/main.go:13` ("Every name here is implemented") must match before the change. A search
  that returns nothing *before* the change is a broken search, not a converged tree — `rg` uses Rust
  regex, so `\|` is a literal pipe and silently matches nothing.
- **Claim-bearing hits this bead amends:** `docs/context/architecture.md:50` ("Eleven subcommand
  implementations") and `docs/context/cli-and-tooling.md:6` ("All eleven names are implemented"),
  plus the dispatch-map prelude in `cmd/lens/main.go`.
- **Not hits:** `internal/cli/format.go`'s generic "subcommand" mentions — the word, not the count.
- Cross-check the result against `cmd/lens/main.go`'s map, which is the source of truth.

## Rationale

Every destructive action in this ticket converges here, and all three of the plan's review-round
findings about thresholds landed in this file's config/threshold seam. That is why this is the bead
to implement guard-tests-first: the guards are not input validation around a feature, they *are* the
feature's safety, and each one is one boolean away from a whole-table delete.

Opening the store directly is the deliberate exception to the replay precedent (D9), justified by
the use case — you purge when you want the disk back, which is when you least want to start a
server. WAL plus `busy_timeout` is the same protocol every other external opener relies on.

## Outcome Definition

- `lens purge --older-than 0` and `--older-than -1` are refused with the route's non-positive-`days`
  message, exit non-zero, and delete nothing.
- With `retention_days = 0` and no explicit `--older-than`, the implied default is refused rather
  than run as a whole-table delete; a **negative** `retention_days` (`-5`) is refused the same way.
- `--older-than <days> --unpriced` together is refused with a message naming both flags, and deletes
  nothing — never OR-ed into one predicate, never two sequential purges.
- `--unpriced` alone is unaffected by the days guards.
- Every guard refuses **before** `store.Open` — assert the store was never opened, not just that the
  row count is unchanged.
- `--dry-run` deletes nothing; `--yes` is required to write; `--dry-run` runs no `VACUUM`.
- `--vacuum` runs a `VACUUM` and reports the file size before and after.
- `lens` dispatches `purge`; the dispatch-map prelude accounts for twelve names.
- The subcommand-inventory search's **claim-bearing** hit set is empty, with both positive controls
  demonstrated as hits before the change.
- `replay.go`'s prelude no longer implies that no subcommand opens the store.

## Test Specifications

`internal/cli` (`purge_test.go`) — write the guard cases **first**:

1. `--older-than 0` and `--older-than -1`: each refused, non-zero exit, and the store untouched.
   Assert refusal happens **before** `store.Open` by pointing `DBPath` at a path that does **not**
   exist and asserting the error is the guard's message **and** that no file was created at that
   path. A successful `store.Open` creates the database file, so its absence is the observable
   proof that the guard ran first — an implementation that opened the store before validating
   leaves a stray `.db` behind and fails here.
2. `retention_days = 0`, no explicit `--older-than`: refused, nothing deleted.
3. `retention_days = -5`, no explicit `--older-than`: refused the same way — the case that separates
   `<= 0` from `== 0`, and the one that fails if the guard is written as `== 0`.
4. `--older-than 30 --unpriced`: refused with a message naming **both** flags and saying they select
   different rows; nothing deleted.
5. `--unpriced` alone with `retention_days = 0`: **succeeds** — the days guards do not apply.
6. Each guard test asserts the row count is unchanged afterwards (belt; the pre-`store.Open` assertion
   is braces).
7. `--dry-run`: reports a count, deletes nothing, and skips `--vacuum`. Sequence the assertions so
   the skip is observable: purge for real with `--vacuum` and record the file size (it shrinks),
   then re-run with `--dry-run --vacuum` and assert the size is **unchanged** — a run that merely
   reports a count would still shrink the file if `--vacuum` were not gated on the dry run.
8. `--yes` gate: without it, no write; with it, the write happens.
9. `--vacuum`: the DB file shrinks after a purge, and the reported before/after sizes differ.
10. Both predicates end to end: an aged set and an unpriced set, each selected by its own flag.

`cmd/lens` — no test files in this package; the dispatch register is covered by `go build` plus the
search's cross-check against the map.

## Files to Touch

- `internal/cli/purge.go` (create — the subcommand, its `FlagSet`, the three guards, the preview,
  the confirm, the vacuum)
- `internal/cli/purge_test.go` (create — the guard cases first, then the happy paths)
- `cmd/lens/main.go` (modify — register `purge`; amend the "Every name here is implemented" prelude
  for the twelfth subcommand)
- `internal/cli/replay.go` (modify — the "consumer stays the only writer" prelude, `:30-36`)
- `internal/cli/replay_test.go` (modify — the "must not become a second writer" comments)
- `docs/context/architecture.md` (modify — `:50`, "Eleven subcommand implementations", per the
  subcommand-inventory search this bead owns)
- `docs/context/cli-and-tooling.md` (modify — `:6`, "All eleven names are implemented")
