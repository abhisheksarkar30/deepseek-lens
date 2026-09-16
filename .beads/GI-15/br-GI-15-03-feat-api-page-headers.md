# Bead br-GI-15-03: Page headers and `?offset` on the three list endpoints

**Plan Reference**: `docs/planning/GI-15-pagination.md` §API layer, §Design decisions

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-15-02
- **Blocks**: br-GI-15-05

## Description

Turn the store primitives into a paginated HTTP surface. Response **bodies stay bare JSON arrays** —
pagination metadata rides on response headers, which is the non-breaking choice: every existing
consumer (`fetchJSON`, the `api_test.go` assertions, the CLI) keeps working untouched.

**New `parseOffset(r) (int, error)`** next to `parseLimit`: same shape, defaults to 0 when absent,
rejects a negative value with an error the handler turns into 400 — mirroring how a negative
`?limit` is already handled. Because negatives are rejected here, the handler never needs to resolve
an effective offset: absent → 0 *is* the effective offset.

**New `writePageHeaders(w, total, limit, offset int)`** setting `X-Total-Count`, `X-Limit`,
`X-Offset`. It must be called **before** `writeJSON` — `writeJSON` calls `WriteHeader`, after which
headers are silently dropped and the bug surfaces only as three missing headers in a browser.

**`X-Limit` reports the EFFECTIVE limit, never the requested one. This is the trap this bead
exists to avoid.** An absent `?limit` parses to `0`, which the store clamps to `DefaultLimit`
(1000) — so the response must carry `X-Limit: 1000`, **not** `X-Limit: 0`. A header-driven consumer
computes `nextOffset = offset + X-Limit`; reporting the raw `0` leaves it stuck on page 1 forever,
with no error to explain why. Each of the three handlers therefore resolves the clamp *before*
calling:

```go
effLimit := limit
if effLimit <= 0 {
    effLimit = store.DefaultLimit
}
```

This duplicates the store's own clamp deliberately — the handler cannot read the clamp back out of
the store, and a header that disagrees with the rows on screen is worse than one line of arithmetic
in two places.

**Three handlers:**

- `listRequests` — parse `offset`, add it to the `store.Filter`; after `ListRequests`, call
  `CountRequests` **with the same filter**, then `writePageHeaders(w, total, effLimit, offset)`,
  then `writeJSON` unchanged.
- `listWarnings` — identical with `CountWarnings`.
- `listSessions` — parse `limit` and `offset`, call
  `ListSessions(ctx, store.Filter{Limit: limit, Offset: offset})`, then `CountSessions`, then
  `writePageHeaders(w, total, effLimit, offset)`. The existing comment claiming there is "no limit
  param to honor" is now false and gets removed — br-GI-15-02 left a `store.Filter{}` call here
  specifically so this bead leaves the plumbing in one place.

`getRequest`/`getSession` are untouched: single-resource endpoints have nothing to page.

**`X-Total-Count` is not transactional, and that is accepted.** `List*` and `Count*` are two
independent queries, and the store's single writer goroutine is inserting rows the whole time the
dashboard is open. A row landing between the two calls makes the total one higher than the page was
computed from — cosmetic (Next may offer a page that turns out empty) and consistent with the
project's fail-open philosophy for a local single-user tool. Wrapping the pair in a `BEGIN DEFERRED`
read transaction is the upgrade path if it ever matters. Do not add one here; the plan considered
and declined it.

## Rationale

This is the smallest API change that makes the endpoints genuinely paginable while keeping every
existing client byte-compatible — which is why the headers approach was chosen over a response
envelope. The effective-limit rule is the part that a straightforward implementation gets wrong:
it looks like it works, because `X-Limit: 0` is only observably broken to a *consumer* that does
the offset arithmetic, and the browser dashboard is not yet such a consumer at this commit.

## Outcome Definition

- `go build ./...`, `go vet ./...`, `go test ./internal/api/...` pass.
- All three list endpoints return `X-Total-Count`, `X-Limit`, `X-Offset`; response bodies are
  unchanged bare arrays.
- `?limit` **absent** on any of the three → `X-Limit == store.DefaultLimit`.
- `?limit=25` → `X-Limit == 25`.
- `?offset=` absent → `X-Offset == 0`; negative `?offset=` → HTTP 400.
- `X-Total-Count` is the full filtered count, independent of `limit`/`offset`.
- The `listSessions` "no limit param to honor" comment is gone.
- No `BEGIN`/transaction is introduced.

## Test Specifications

- Unit Tests (`internal/api/api_test.go`):
  - **Headers present on all three routes**: `/api/requests`, `/api/warnings`, `/api/sessions`
    each return all three headers.
  - **Effective limit — the regression guard for the `X-Limit: 0` bug**: `?limit` absent →
    `X-Limit == store.DefaultLimit` (1000). Assert against the constant, not a literal.
    `?limit=25` → `X-Limit == 25`. Both asserted on all three endpoints.
  - **Offset window**: seed 10 requests; `?limit=5&offset=5` returns the second five, and its ids
    are disjoint from `?limit=5&offset=0`'s. Assert disjointness, not just length.
  - **`X-Offset`**: absent → `0`; `?offset=7` → `7`.
  - **`X-Total-Count` ignores the window**: the same value for `?limit=1` and `?limit=1000`.
  - **`X-Total-Count` honors the predicate**: with a mixed fixture, `/api/warnings?kind=X` reports
    the count of X only, and it equals the length of the same call at a limit above the seed.
  - **Negative offset → 400**: `?offset=-1` on all three routes, mirroring the existing
    negative-limit test.
  - **`/api/sessions` now caps**: seed more than `DefaultLimit` sessions and assert the response
    body has exactly `DefaultLimit` items while `X-Total-Count` reports the true, larger total.
    (Seeding past 1000 is slow; if the fixture cost is prohibitive, assert with a small `?limit=`
    against a small seed and cover the clamp through the effective-limit case above.)
  - **Bodies unchanged**: existing array-shaped decoding still succeeds — this is what proves the
    headers approach did not become an envelope.
- Integration Tests: none beyond the above; the UI consumer arrives in br-GI-15-05.

## Files to Touch

- `internal/api/api.go` (modify — `parseOffset`, `writePageHeaders`, the three handlers, the
  removed stale comment)
- `internal/api/api_test.go` (modify — the cases above)

## Review Notes

**Deviation (OK).** The tests went into `internal/api/pagination_test.go` (9 cases) rather than into
`api_test.go`, matching what br-GI-15-01 did on the store side.

**Verified live, not only in tests.** Against a fresh `go build` on a throwaway port and temp DB:

```
GET /api/requests                       → X-Total-Count: 0  X-Limit: 1000  X-Offset: 0
GET /api/warnings                       → X-Total-Count: 0  X-Limit: 1000  X-Offset: 0
GET /api/sessions                       → X-Total-Count: 0  X-Limit: 1000  X-Offset: 0
GET /api/requests?limit=25&offset=0     → X-Limit: 25
GET /api/sessions?limit=7               → X-Limit: 7
GET /api/requests?offset=-1             → 400
```

The `X-Limit: 1000` on an absent `?limit` is this bead's whole reason to exist, confirmed on the
wire on all three routes rather than inferred from `effectiveLimit`'s source.

**One case the bead allowed me to cover indirectly.** "`/api/sessions` now caps: seed more than
`DefaultLimit` sessions" is prohibitively slow to seed at the API layer; `TestListSessionsDefaultClamp`
covers the store-side clamp and `TestPageHeaderLimitIsEffective` covers the header value on all three
routes, which is what the bead's parenthetical sanctions.

**Accepted, not fixed.** `List*` and `Count*` are two independent queries, so a row inserted between
them can make `X-Total-Count` one higher than the page was drawn from. The bead considered and
declined a `BEGIN DEFERRED` read transaction; no transaction was added. The dashboard handles the
consequence rather than the server preventing it — see br-GI-15-05's `emptyHint`.
