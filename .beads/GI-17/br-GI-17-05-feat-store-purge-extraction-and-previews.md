# Bead br-GI-17-05: `purgeWhere`, the two preview read pairs, and `Vacuum`

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Design decisions D5, D6, D7

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-17-02 (for `store.PurgeResult`)
- **Blocks**: br-GI-17-06, br-GI-17-07, br-GI-17-08

## Description

Everything the purge needs below the HTTP layer: one shared delete implementation, the preview
reads that make a confirmation informed, the vacuum escape hatch, and the D7 invariant amendment's
source-comment half.

**1. Extract `purgeWhere`, and make both public methods thin wrappers.**

```go
func (s *Store) purgeWhere(ctx context.Context, where string, args ...any) (PurgeResult, error)
```

It holds the transaction, the warnings delete, the per-affected-session `reconcileSession` loop
([store.go:893-897](../../internal/store/store.go#L893-L897)), and the commit.
`PurgeOlderThan(ctx, cutoff) (PurgeResult, error)` and the new `PurgeUnpriced(ctx) (PurgeResult, error)`
differ **only** in their WHERE clause and become wrappers over it. The session-reconciliation logic
is the part most likely to be fixed in one copy and forgotten in the other, so keeping exactly one
copy is the point of the extraction.

The existing warnings delete is
`DELETE FROM warnings WHERE request_id IN (SELECT id FROM requests WHERE started_at < ?)`
([store.go:878-881](../../internal/store/store.go#L878-L881)) — generalize the inner `WHERE` to the
passed clause rather than duplicating the statement.

`PurgeResult` is **already declared** by br-GI-17-02 in `internal/store/types.go`. Do not re-declare
it. The second field cannot be dropped: `POST /api/purge` returns `sessions_reconciled` for **both**
modes, and only the loop that walks `affected` knows how many sessions were touched — a
`(int64, error)` method cannot report it, so a handler serving `sessions_reconciled` for the
older-than mode is unimplementable against the old shape.

`PurgeOlderThan`'s two existing call sites
([store_test.go:582](../../internal/store/store_test.go#L582),
[:657](../../internal/store/store_test.go#L657)) are updated to read `res.Deleted` — a same-package
change, still asserting the same deleted count and the same session reconciliation.

**2. The D5 predicate lives in exactly one place.**

```sql
COALESCE(cost_source, 'unpriced') = 'unpriced'
  AND (input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) > 0
```

Hold it as a shared `store` constant plus a `purgeUnpricedWhere` helper, so the preview count and
the delete predicate agree **by construction** rather than by two copies staying in sync.

`cost_usd IS NULL` alone is a **superset** of "unpriced": it also matches every `unknown-model` row,
because `Compute` returns `Cost{Source: SourceUnknownModel}` with a nil `Amount`
([pricing.go:140-148](../../internal/pricing/pricing.go#L140-L148)), and the dashboard presents
`unknown-model` and `unpriced` as distinct groups. An irreversible "purge unpriced" driven by the
bare NULL test would silently destroy a class of rows the user was never shown, and the preview's
one number would hide it. So start from the `COALESCE` condition `StatsByCostSource` already uses to
label a row `unpriced` ([store.go:533-540](../../internal/store/store.go#L533-L540)), which folds in
the NULL-source rows while leaving `unknown-model`, `configured` and `approximate` as their own
groups. Precedence matters: a bare `<>` test is NULL-safe-false and would wrongly drop the
NULL-source rows, which is why the `COALESCE` form is used.

The positive-token conjunct is what makes this a strict **subset** of the Stats tab's `unpriced`
group rather than an equality with it: `StatsByCostSource` groups with **no** token condition, so
the Stats tab counts zero-token error/4xx responses that this predicate deliberately excludes. The
two on-screen numbers differ **by design**, by exactly those rows. The conjunct is also what makes
the purge *"records of actual tokens used"* — without it, error responses that were never priceable
would be destroyed too. There is no `tokens` column, so the conjunct is spelled out over the
schema's four token columns
([schema.sql:23-26](../../internal/store/schema.sql#L23-L26)).

**3. Two preview read pairs, each named for its own predicate.**

```go
CountPurgeable(ctx context.Context, cutoff time.Time) (count int, oldest, newest *time.Time, err error)
PurgeableBytes(ctx context.Context, cutoff time.Time) (int64, error)
CountUnpriced(ctx context.Context) (int, error)
UnpricedBytes(ctx context.Context) (int64, error)
```

They are **not** interchangeable: the older-than pair uses the same `started_at < cutoff` predicate
as `PurgeOlderThan`, the D5 pair the same `COALESCE(…) AND tokens > 0` predicate as
`PurgeUnpriced`. Two read methods named for one predicate cannot produce both the `eligible_*` and
`unpriced_*` pairs the Settings tab shows at once.

- **Both byte reads are `COALESCE(SUM(LENGTH(req_body)+LENGTH(resp_body)), 0)`** — the same house
  idiom the existing `SUM` aggregates use. `SUM` over zero rows is SQL `NULL`, which cannot scan
  into an `int64`, and zero eligible rows is a supported state rather than a corner: it is what a
  `days` larger than the data's age produces, and what an install with no unpriced rows has.
  *Ceiling*: linear in eligible rows; fine at this tool's scale. Approximate, and stated as such: a
  row whose bodies are `NULL` contributes nothing (`LENGTH(NULL)` is `NULL` and `SUM` skips it), so
  body-less rows are undercounted.
- **`CountPurgeable` scans `MIN`/`MAX(started_at)` into a nullable type** (`*time.Time`, `nil` when
  the eligible count is `0`), because `MIN`/`MAX` over an empty set is SQL `NULL` and cannot scan
  into a value `time.Time` — the same trap `reconcileSession` avoids with `sql.NullInt64`
  ([store.go:917-924](../../internal/store/store.go#L917-L924)). `oldest`/`newest` are `null`
  **whenever the eligible count is 0**, not only when `days <= 0`.

**4. `(*Store).Vacuum(ctx) error`.** A `VACUUM` on the store's **writer** connection — the `writer`
field is unexported, so this method is the only path to it. It is opt-in and never automatic (R3):
`VACUUM` needs free space on the order of the DB size and takes an exclusive lock that blocks
ingest, which is why it is not in the seam br-GI-17-02 declared for the API. The dashboard never
offers it.

**5. Amend the `store.go` package doc — and own the D7 single-writer search.**

CLAUDE.md states SQLite's only writer is the consumer goroutine. A purge necessarily runs off that
goroutine. There are **two** purge writers, and the amended invariant names both honestly:

- **In-process purge** (the scheduled/startup run and `POST /api/purge`) uses the store's writer
  connection, which is `SetMaxOpenConns(1)`
  ([store.go:105](../../internal/store/store.go#L105)), so it queues behind ingest rather than racing
  it.
- **`lens purge`** (br-GI-17-08) is a *separate process with its own writer connection*. It is **not**
  serialized by the in-process `SetMaxOpenConns(1)`; it is serialized cross-process by WAL plus
  `busy_timeout(5000)` ([store.go:43](../../internal/store/store.go#L43)).

The honest sentence is: *row ingest has exactly one writer; purges are the other writers —
in-process ones sharing the single writer connection, and the `lens purge` process serialized
cross-process by WAL + `busy_timeout`*. Do **not** publish a "the purge path is serialized through
the same single connection" claim: it is false for `lens purge`.

Amending the invariant means amending **every** statement of it — search, not sample. This bead owns
the **single-writer governing search** (`docs/planning/GI-17-pricing-retention-purge.md` §D7
governing searches, search 1), run whole-tree with the frozen-path exclusions
(`-g '!docs/planning/GI-*' -g '!docs/superpowers/specs' -g '!.beads/GI-1' -g '!.beads/GI-4' -g '!.beads/GI-15'`):

```
rg -n -i -g '!docs/planning/GI-*' -g '!docs/superpowers/specs' \
      -g '!.beads/GI-1' -g '!.beads/GI-4' -g '!.beads/GI-15' \
      -e 'exactly one goroutine' \
      -e 'only the consumer goroutine' \
      -e 'ever calls a writer method' \
      -e 'consumer (stays|remains) the (only|sole) writer' \
      -e "the consumer's single writer" \
      -e 'sqlite has (exactly )?one writer' \
      -e 'must not become a second writer'
```

- **Positive control, asserted before the empty result counts**: `store.go:4` ("exactly one
  goroutine every calls a writer method" — [store.go:4-6](../../internal/store/store.go#L4-L6)) must
  match. `rg` is Rust regex: `|` alternates, and `\|` is a **literal pipe** that matches nothing, so
  a pattern written with `\|` silently no-ops the gate. A search that returns nothing *before* the
  change is a broken search, not a converged tree.
- **Claim-bearing hits this bead amends**: `store.go`'s package doc (the positive control itself)
  and `internal/api/api.go:455-456`, the `sendReplay` doc that quotes CLAUDE.md's single-writer
  sentence. That line is the *single-writer* claim; the read-only owners (br-GI-17-02/04) do **not**
  own it.
- **Declared true lines that stay** — named here so they read as deliberate, not as oversights:
  `store.go:75` ("opened with one writer connection" — the connection cap, true; not matched),
  `consumer.go:94` (the consumer's single `Run` goroutine — true; not matched), `consumer_test.go`
  `:498`/`:546` (struct-field ownership — true; not matched), `docs/context/architecture.md:46` (the
  `SetMaxOpenConns(1)` config — true; not matched), `docs/context/data-model.md:103-104` (the
  connection facts; the claim-bearing lines of that same section are `:105-:107`, which the pattern
  *does* match and amend), `docs/context/testing-and-quality.md:72` (the
  `TestConcurrentInsertsSerialize` label — true; not matched), and `api.go:326`/`:354` ("the
  consumer's single writer" describing replay traffic — true for replay, and *matched*, so it must
  be named here to stay).

The searches over-match on purpose — a stray "read-only" is a hit to *read*, not to skip. The source
of truth is the writer connection in `internal/store`, not the grep.

**6. A concurrency test that asserts what it can.** A new test runs a purge and an `InsertRequest`
concurrently under `-race` and asserts both calls return nil and every row is accounted for —
present or deleted, none lost or duplicated. It is **not** a race detector for those two calls:
`s.writer` is capped at one open connection
([store.go:105-106](../../internal/store/store.go#L105-L106)), so `database/sql` serializes them
before any Go-memory race can exist, and `*sql.DB` is concurrency-safe by contract. The
serialization guarantee is `SetMaxOpenConns(1)`'s, not the test's; the test pins the no-lost-row
behaviour that guarantee is meant to produce. (`TestReaderDoesNotBlock`, `store_test.go:523-556`, is
this repo's shape of a contention test that *can* fail.) Say this in the test's comment so the next
reader does not mistake it for a race proof.

## Rationale

One delete implementation, two predicates, one result type. Every change here is forced by the
destructive operation sitting on top of it: the preview must agree with the delete by construction,
the delete must reconcile sessions, and the invariant the repo documents must say what the code now
does. This is the lowest bead in the ticket and the one every other purge bead depends on.

## Outcome Definition

- `PurgeUnpriced` deletes exactly the D5 predicate's rows; priced rows, `unknown-model` rows, and
  **unpriced rows with zero tokens** all survive.
- `PurgeOlderThan` and `PurgeUnpriced` share one `purgeWhere`; neither contains its own transaction,
  warnings delete, or reconciliation loop.
- `PurgeResult.Deleted` counts only deleted requests; `SessionsReconciled` the distinct sessions
  touched. Affected sessions are reconciled, and a session left with no rows is deleted.
- Over an empty eligible set: `CountPurgeable` returns `count 0` with `oldest`/`newest` `nil`,
  `CountUnpriced` returns `0`, and both byte reads return `0` rather than an error.
- Purging an empty match returns `PurgeResult{}, nil` and touches no session row.
- `(*Store).Vacuum(ctx)` exists and runs `VACUUM` on the writer connection.
- The D5 predicate appears once in the package, as a constant used by both the reads and the delete.
- The single-writer search's **claim-bearing** hit set is empty, with `store.go:4` demonstrated as a
  hit before the change.

## Test Specifications

`internal/store` (`store_test.go`):

1. `TestPurgeUnpriced`: seed priced rows, `unknown-model` rows, unpriced rows with tokens, and
   unpriced rows with zero tokens. Assert only the last-but-one class is deleted — the
   discriminating test for the whole predicate, and the one that fails if the conjunct is dropped.
2. `PurgeResult`: `Deleted` counts only requests; `SessionsReconciled` counts distinct affected
   sessions, and a session left with no rows is removed.
3. `PurgeOlderThan` updated to `res.Deleted` at both existing call sites, still asserting the same
   deleted count and reconciliation — the regression guard for the `purgeWhere` extraction.
4. Preview-vs-delete agreement, both predicates: for each of the two pairs, assert the preview's
   count and bytes equal what the corresponding purge then deletes. This is the "agree by
   construction" claim as a test rather than as a comment.
5. Empty eligible set: a cutoff older than every row → `count 0`, `oldest`/`newest` `nil`,
   `PurgeableBytes` `0`, no error. No unpriced rows → `CountUnpriced` `0`, `UnpricedBytes` `0`.
6. Non-interchangeable pairs: with both an aged set and an unpriced set present, assert
   `CountPurgeable` and `CountUnpriced` differ — proving the two pairs are wired to different
   predicates.
7. Concurrent purge + insert under `-race`: both return nil and every row is accounted for. Comment
   that the guarantee is `SetMaxOpenConns(1)`'s, not the race detector's.

## Files to Touch

- `internal/store/store.go` (modify — `purgeWhere` extraction, `PurgeUnpriced`, the D5 predicate
  constant + `purgeUnpricedWhere`, the four preview reads, `(*Store).Vacuum`, package-doc
  amendment, plus the claim-bearing hits the single-writer search finds)
- `internal/store/store_test.go` (modify — the cases above, and the two `PurgeOlderThan` call sites)
- `internal/api/api.go` (modify — the `sendReplay` doc's single-writer quote, `:455-456`, per the
  D7 single-writer search this bead owns)
