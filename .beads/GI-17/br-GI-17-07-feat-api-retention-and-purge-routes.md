# Bead br-GI-17-07: `GET /api/retention` and `POST /api/purge`

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Contracts §`GET /api/retention`, §`POST /api/purge`, §Design decisions D10, D11

- **Priority**: P1 (high)
- **Dependencies**: br-GI-17-02, br-GI-17-05
- **Blocks**: br-GI-17-10

## Description

The preview and the delete. They are one bead because the whole guarantee is that the number the
user confirms is the set the delete acts on — splitting them would let the two drift with each half
looking correct.

**`GET /api/retention` is a read.** It deletes nothing, so the confirmation the user clicks is
informed by a real number rather than by an estimate.

```json
{"days":30,"cutoff":"2026-08-17T00:00:00Z","eligible_requests":412,
 "eligible_bytes":52428800,"oldest":"2026-07-01T09:12:00Z","newest":"2026-08-16T23:58:00Z",
 "unpriced_requests":37,"unpriced_bytes":1048576}
```

Both previews come back in one response because the Settings tab shows both actions at once. The
response is built by a small **tagged struct in `internal/api`** — not by serving a `store` type
directly, since `internal/store` carries no json tags on purpose
([types.go:57-61](../../internal/store/types.go#L57-L61)).

The states, which are contract rather than presentation:

- **`days <= 0` ("keep forever"):** `eligible_requests` and `eligible_bytes` are `0`,
  `oldest`/`newest` are `null`, and `cutoff` is **`null`** — no threshold is configured, so nothing
  is eligible and there is no instant to name. `cutoff` is never a zero timestamp and never an echo
  of `now`. `null` is the same "unset" convention D3 uses for a rate, and the Settings tab renders
  it as the "keep forever" state.
- **`days > 0`:** `cutoff = now − days` as an RFC3339 string, and the eligible set is
  `started_at < cutoff`.
- **`days` larger than the data's age:** that same empty-set state **with `cutoff` set** —
  `eligible_requests`/`eligible_bytes` `0`, `oldest`/`newest` `null`. `oldest`/`newest` are `null`
  **whenever the eligible count is `0`**, not only when `days <= 0`.

**`eligible_*` must never be computed from `cutoff = now`.** That would present the whole table as
eligible and put a "delete everything" button one click away under the *safest* setting.

`unpriced_requests`/`unpriced_bytes` come from the **exact D5 predicate** — the same
`COALESCE(cost_source,'unpriced')='unpriced' AND (four token columns) > 0` the purge deletes on — so
the preview class and the delete class agree by construction and the `unknown-model` bucket is never
counted or deleted. They do **not** depend on `days`; they report whether or not retention is
configured, so the unpriced action stays available when retention is off.

**The two numbers are deliberately different from the Stats tab's.** `unpriced_requests` is a strict
**subset** of the Stats tab's `unpriced` group (which has no token condition and so also counts
zero-token rows); they differ by exactly those rows and are not expected to be equal. This is why
the Settings-tab confirm copy must label its number as the positive-token count — see
br-GI-17-10.

**`POST /api/purge` is the write.** Body `{"mode":"older_than","days":30}` or
`{"mode":"unpriced"}`; `days` is ignored for `unpriced`. Returns
`{"mode":…,"deleted":<n>,"sessions_reconciled":<n>}`, both from `PurgeResult` (D6).

| Status | Cause |
|---|---|
| 400 | missing or unknown `mode`, or a non-positive `days` |
| 403 | `replayOriginReject` (non-loopback `Host`, or cross-origin), action name `purge` |
| 503 | retention seam unwired |

A `days` value larger than the data's age is **not** an error — it deletes nothing and returns
`deleted: 0`, the same empty-set state the preview reports.

Two modes on one route because the guard, the confirm and the "report what was deleted" response are
identical; only the WHERE clause differs (D10).

**Never a GET.** The route is reachable only by `POST`, and the guard test asserts it.

## Rationale

This is where the ticket's irreversible action becomes reachable from a browser, so the preview and
the delete share a bead, a predicate constant and a test suite. The `cutoff: null` case is the
specific one worth the ceremony: it is the difference between "retention is off, nothing is
eligible" and "everything is eligible" — the two readings of an unconfigured threshold, one of
which deletes the user's whole capture file.

## Outcome Definition

- `GET /api/retention` reports counts and deletes nothing — the row count is unchanged after calling
  it.
- Its `unpriced_requests` equals what `PurgeUnpriced` actually deletes.
- `days = 0`: `eligible_requests`/`eligible_bytes` `0`, `oldest`/`newest`/`cutoff` all `null`, and
  no `cutoff`-wide set is offered.
- `days > 0`: `cutoff` is the RFC3339 instant `now − days`, and the eligible set is
  `started_at < cutoff`.
- `days > 0` with **zero** eligible rows: 200 with `eligible_requests`/`eligible_bytes` `0` and
  `oldest`/`newest` `null`, not an error. An install with no unpriced rows likewise answers `0`.
- `POST /api/purge` deletes exactly the previewed class for each mode, and returns
  `deleted`/`sessions_reconciled` for both.
- Both routes answer `503` with the retention seam unwired.
- Both reject a cross-origin `Origin` and a non-loopback `Host` with 403 naming the `purge` action.

## Test Specifications

`internal/api` (`purge_test.go`):

1. **Preview-vs-delete, both modes** — the test this bead exists for. Seed rows, `GET` the preview,
   then `POST` the matching purge, and assert the deleted count equals the previewed count. Repeat
   for `unpriced`.
2. Preview is a read: assert the total row count is unchanged after the `GET`.
3. `days = 0`: the three nulls (`oldest`, `newest`, `cutoff`) and the two zeros.
4. `days > 0`, empty eligible set (a `days` larger than the data's age): 200, zeros, nulls — and
   `cutoff` **present**, which is what distinguishes this state from `days = 0`.
5. No unpriced rows: `unpriced_requests`/`unpriced_bytes` `0`, no error.
6. `unknown-model` rows are **not** counted in `unpriced_requests` — the assertion that fails if the
   predicate degrades to a bare `cost_usd IS NULL`.
7. `unpriced_*` are independent of `days`: the same numbers with `days` unset and set.
8. `POST`: `mode: "older_than"` with a valid `days` deletes; `mode: "unpriced"` deletes; a missing or
   unknown `mode` is 400; `days: 0` and `days: -1` with `older_than` are 400 and delete nothing.
9. `POST` with a `days` larger than the data's age returns 200 with `deleted: 0`.
10. Guard: cross-origin `Origin` → 403; non-loopback `Host` → 403; both bodies name the `purge`
    action, not `replay`.
11. Unwired: both routes 503 without `SetRetention`.
12. Method: a `GET /api/purge` is not a route.

## Files to Touch

- `internal/api/purge.go` (create — both handlers, the tagged preview struct, the mode decode, route
  registration)
- `internal/api/purge_test.go` (create — the cases above)
