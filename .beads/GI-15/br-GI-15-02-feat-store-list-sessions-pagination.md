# Bead br-GI-15-02: Paginate `ListSessions` and absorb its signature change

**Plan Reference**: `docs/planning/GI-15-pagination.md` §Store layer (`ListSessions`)

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-15-01
- **Blocks**: br-GI-15-03

## Description

`ListSessions` is the one fully unbounded read in the store — `SELECT ... ORDER BY last_seen DESC`
with no `LIMIT` at all. Give it the same shape as the other two lists:

```go
func (s *Store) ListSessions(ctx context.Context, f Filter) ([]*Session, error)
```

It honors only `Limit`/`Offset` (the "only the fields relevant to the call being made are read"
convention `Filter`'s doc comment already states), applies the same `Limit <= 0 → DefaultLimit`
clamp, and adds `LIMIT ? OFFSET ?` plus `id DESC` to its `ORDER BY` — that last part is the F6.1 fix
already applied to requests and warnings in br-GI-15-01, and it matters here for the same reason:
two sessions with equal `last_seen` can otherwise be served on two pages or dropped across a
boundary. `sessions.id` is TEXT, but lexicographic order is still a total order, which is all the
tiebreaker needs to be.

Also add `CountSessions(ctx) (int, error)` → `SELECT COUNT(*) FROM sessions`. It takes **no**
`Filter`: no `Filter` field is defined for sessions (`ListSessions` honors only `Limit`/`Offset`, and
`Filter`'s predicate fields — `SessionID`, `Model`, `OnlyWarned`, `OnlyErrors`, `Kind`, `Severity`,
`ReplayOf`, `Since` — all target requests or warnings, not sessions). So the count is the whole
table, and `ListSessions` with the window ignored is its only reader.

Do not give it a `Filter` "for symmetry" — that would invent a rule for what it ignores.

**This is a real behavior change, and it is the point.** Sessions were previously truly unbounded;
they are now capped at `DefaultLimit` (1000) by default like every other list. 1000 sessions is far
beyond what a single-developer tool accumulates before the lack of a cap matters, so consistency
wins over the current unboundedness — but the change is not free and is not hidden (see the
`lens sessions` note below).

**The signature change is the coupling in this bead.** `ListSessions(ctx)` → `ListSessions(ctx,
Filter)` does not compile anywhere it is called, and one of those callers is the `api.Store`
interface — so this bead owns the whole ripple, not just the store method:

- `internal/api/api.go` — the `Store` interface's `ListSessions` method, **and** the `listSessions`
  handler's call, which becomes `s.ListSessions(ctx, store.Filter{})`. A zero-value `Filter` is
  the behavior-preserving form: it means "default limit, offset 0", i.e. exactly what the method did
  before. br-GI-15-03 parses real `?limit`/`?offset` into that call; do not pre-empt it here.
- `internal/cli/sessions.go:36` — `ListSessions(ctx, store.Filter{})`.
- `internal/consumer/consumer_test.go:786,831` — same.
- `internal/cli/cli_test.go:356` — same.
- `internal/api/publishing_store.go` — **no edit needed.** `PublishingStore` embeds `*store.Store`
  and overrides only write methods, so it inherits the new signature automatically. Do not add a
  forwarding stub.

**Accepted side effect (recorded, not fixed).** The `lens sessions` CLI command silently goes from
"every session" to "the first 1000, newest first". The plan accepts this: the `ListSessions(ctx,
store.Filter{})` fixup is a mechanical compile fix, and adding a `--limit` flag to `lens sessions`
is a follow-on improvement, not part of this ticket. Do not add the flag here.

**Doc comments this bead owns:**

- `internal/store/store.go:543` — `"ListSessions returns every session, most recently active
  first."` → `"ListSessions returns a page of sessions, most recently active first."` It returns a
  page now, and "every session" is false the moment the clamp lands.
- `internal/store/store.go:30-32` — the `DefaultLimit` comment enumerates what `Filter.Limit: 0`
  means; add `ListSessions` to that list.
- `internal/store/types.go:102-104` — **complete** the rewrite br-GI-15-01 started: add
  `ListSessions` to the narrowing readers (noting it ignores everything but `Limit`/`Offset`) and
  `CountSessions` to the count enumeration (noting it takes no `Filter`). After this bead the
  comment names all four `Filter` readers and all three counts, so the sentence is finally whole.

## Rationale

An unbounded `SELECT` on the sessions table is the same class of problem as the unbounded warnings
list, just further from the surface: it is the only list endpoint with no cap at all, so it is the
one guaranteed to degrade, and it is what makes `GET /api/sessions` unpaginable. Doing it in its
own bead — rather than folded into the API work — is what keeps the mechanical call-site churn
separable from the API design, and lets the tiebreaker and clamp be tested against the store
directly.

`CountSessions` takes no `Filter` deliberately. It would be easy to give it one "for symmetry" and
then have to invent a rule for what it ignores; the honest signature says sessions have no
filterable column.

## Outcome Definition

- `go build ./...`, `go vet ./...`, and `go test ./...` all pass — the last one is the real check,
  since the call-site fixups live in `internal/cli` and `internal/consumer` tests.
- `ListSessions` takes a `store.Filter`, honors `Limit`/`Offset`, clamps `Limit <= 0` to
  `DefaultLimit`, and orders by `last_seen DESC, id DESC`.
- `CountSessions` exists, takes only a `ctx`, and returns the total session count.
- `api.Store`'s `ListSessions` signature matches, and `PublishingStore` satisfies it with no new
  code.
- No call site of `ListSessions` passes anything but `store.Filter{}` yet.
- `lens sessions` still builds and runs; its output is now capped at 1000 with no new flag.
- All three doc comments above read true.

## Test Specifications

- Unit Tests (`internal/store/store_test.go`):
  - **Offset window**: seed 6 sessions; `{Limit: 3, Offset: 0}` and `{Limit: 3, Offset: 3}` are
    disjoint and together cover all 6.
  - **Default clamp**: `store.Filter{}` and `{Limit: 0}` both return at most `DefaultLimit` rows —
    this is the "no longer unbounded" assertion.
  - **Offset past the end** returns empty, no error.
  - **Duplicate `last_seen` tiebreaker**: seed two or more sessions with **identical** `last_seen`
    and page with `Limit: 1`; assert every session appears exactly once across the pages and none
    is dropped. Identical timestamps are required for this test to have teeth.
  - **`CountSessions`** equals the seeded total and is unaffected by any `Filter`.
- Integration / compile-level: `go test ./...` green is the assertion that the `internal/cli` and
  `internal/consumer` call sites were all updated — a missed one is a build failure, not a silent
  wrong answer.

## Files to Touch

- `internal/store/store.go` (modify — `ListSessions` signature, `LIMIT ? OFFSET ?`, `id DESC`,
  `CountSessions`, the two `store.go` doc comments)
- `internal/store/types.go` (modify — complete the `types.go:102-104` comment)
- `internal/store/store_test.go` (modify — the cases above)
- `internal/api/api.go` (modify — `Store` interface signature; `listSessions` passes
  `store.Filter{}`)
- `internal/cli/sessions.go` (modify — call-site fixup)
- `internal/consumer/consumer_test.go` (modify — two call-site fixups)
- `internal/cli/cli_test.go` (modify — call-site fixup)

## Review Notes

**Verified.** `PublishingStore` needed no edit, as predicted — it embeds `*store.Store` and
overrides only write methods, so it inherits the new signature (`grep ListSessions
internal/api/publishing_store.go` is empty). All five call sites pass `store.Filter{}` and nothing
else, which is this bead's "do not pre-empt br-GI-15-03" condition. The comment `Filter`'s doc block
completes in two steps exactly as the split-comment note planned: br-GI-15-01 wrote the three
readers that existed then, this bead adds `ListSessions` and `CountSessions`, and the sentence now
names all four readers and all three counts.

**Behavior change, restated because it is the one a reviewer will want to weigh.**
`lens sessions` silently went from "every session" to "the first 1000, newest first". Accepted by the
plan, recorded in the bead, and not hidden: no flag was added, and the output change is the price of
the only unbounded read in the store becoming bounded like its siblings. 1000 sessions is far past
what a single-developer tool reaches before the cap matters.

**Verified by the suite rather than by inspection.** `go test ./...` green *is* the assertion that
every `internal/cli` and `internal/consumer` call site was updated — a missed one is a build
failure, not a silent wrong answer. It passes.

**Deviation (OK).** Same as br-GI-15-01: the test cases live in `internal/store/pagination_test.go`,
not `store_test.go`.
