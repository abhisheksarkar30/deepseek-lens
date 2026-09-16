# Bead br-GI-15-04: `GET /api/warnings/summary`

**Plan Reference**: `docs/planning/GI-15-pagination.md` §API layer (`warningsSummary`)

- **Priority**: P0 (critical)
- **Dependencies**: br-GI-15-01
- **Blocks**: br-GI-15-06

## Description

Add the route the warnings tab will group from, and retire the two comments that claim the
dashboard does the grouping.

**Handler + route.** `warningsSummary` parses `since`/`kind`/`severity` — **no** `limit`/`offset` —
into a `store.Filter`, calls `store.WarningSummary`, and `writeJSON`s the resulting
`[]store.WarningGroup`. Register it as `GET /api/warnings/summary` alongside the other routes in
`New`. **Register it before or alongside `/api/warnings`** and confirm the two coexist: with Go's
`net/http` `ServeMux` the literal path wins over the prefix, so a request to
`/api/warnings/summary` must not fall through to the `/api/warnings` handler. Verify this with a
request in the tests rather than by reading the mux rules — that is the failure that would ship
silently, serving a bare warning array where the dashboard expects groups.

There is deliberately no limit or offset here. A `GROUP BY` result set is bounded by the number of
distinct `(kind, severity)` pairs, not by row count, so it cannot grow with the table — and a
paginated summary would put the UI right back where this ticket found it, unable to state a true
group total. The *query* is unbounded (it must scan every warning to count correctly, which is the
bug fix); the *response* is not. The covering index from br-GI-15-01 is what keeps that scan cheap.

**Two stale comments, both about grouping ownership:**

- `internal/api/api.go:66-68` — `New`'s doc comment says grouping "is the dashboard JS's job". That
  is now wrong: grouping happens in `store.WarningSummary`, in SQL. Remove or retarget the
  parenthetical. The handler stays thin (it only calls `store.WarningSummary`), so the surrounding
  "no business logic in handlers" principle is untouched — it is only the claim about *where*
  grouping lives that has moved.
- `internal/api/api.go:662` — `sessionDetail.Warnings` says warnings are grouped "one line per kind
  is the dashboard's job (see this package's New doc comment)". The claim itself stays true
  (`groupSessionWarnings` survives this story), but the `(see this package's New doc comment)`
  pointer dangles once the edit above removes the grouping discussion from `New`. Drop or retarget
  the parenthetical in the same change.

Both edits belong to this bead rather than br-GI-15-03 because this is the change that moves
grouping server-side; a reviewer reading either comment after 03 alone would still find it true.

## Rationale

Grouping and pagination genuinely do not compose: a page of N rows cannot produce a correct global
per-kind count, and that is true no matter how large N is. So the summary has to come from SQL over
the whole table — which is also why this fixes a latent undercount bug rather than merely
refactoring. Today the dashboard groups at most 1000 raw rows, so any kind with more than 1000
occurrences total has reported a wrong count to every user of the warnings tab. This route is where
that becomes correct.

It is also deliberately its own bead, ahead of the UI switch: the endpoint can be built and tested
against a fixture without touching `app.js`, so the >1000 regression test lands before the code that
depends on it.

## Outcome Definition

- `go build ./...`, `go vet ./...`, `go test ./internal/api/...` pass.
- `GET /api/warnings/summary` returns a JSON array of `WarningGroup` objects with capitalized keys
  (`Kind`, `Severity`, `Count`, `LastSeen`).
- The route does not shadow or get shadowed by `/api/warnings`; both respond correctly to their own
  requests.
- `since`/`kind`/`severity` narrow the summary; no `limit`/`offset` is accepted or needed.
- Groups are ordered highest `Count` first, ties by `kind` then `severity`.
- The `New` doc comment no longer names the dashboard JS as the grouping owner, and the
  `sessionDetail.Warnings` pointer no longer dangles.

## Test Specifications

- Unit Tests (`internal/api/api_test.go`):
  - **Undercount regression (>1000)**: hand-seed more than `store.DefaultLimit` warnings of a single
    kind; `GET /api/warnings/summary` returns one group whose `Count` equals the **true** seeded
    total. This is the test that fails against the behavior this story replaces; it is the reason
    the bead exists.
  - **Grouping shape**: seed a fixture with several kinds and two severities; assert one entry per
    distinct `(kind, severity)` pair with correct counts and `LastSeen` = that group's max
    `created_at`.
  - **Ordering**: groups come back highest-count-first; two kinds with equal counts come back in
    `kind`, `severity` order.
  - **Filters narrow**: `?kind=` and `?severity=` each return only the matching group(s), with
    counts that still reflect the *whole* filtered set; `?since=` excludes older rows.
  - **Route coexistence**: `/api/warnings/summary` returns the group array and `/api/warnings`
    still returns the raw warning array in the same test run — asserted on the response content,
    not on the route table.
  - **No pagination headers**: this route deliberately returns no `X-Total-Count`/`X-Limit`/
    `X-Offset`. Assert their absence so br-GI-15-05's `fetchPage` is never pointed at it.
- Integration Tests: none — the dashboard consumer arrives in br-GI-15-06.

## Files to Touch

- `internal/api/api.go` (modify — `warningsSummary` handler, route registration, the two doc
  comments)
- `internal/api/api_test.go` (modify — the cases above)
