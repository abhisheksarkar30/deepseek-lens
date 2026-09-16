# Bead br-GI-17-02: `*api`, the `SetPricing`/`SetRetention` seams, and a parameterized origin guard

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Design decisions D2, D10

- **Priority**: P0 (critical)
- **Dependencies**: none
- **Blocks**: br-GI-17-03, br-GI-17-04, br-GI-17-07

## Description

Four changes to `internal/api` that every later bead in this ticket builds on. No routes are
registered here — the seam has to exist before a handler can hang off it, and an unwired seam is
what makes the new routes answer 503 rather than panic.

**1. `New` returns `*api`, and `api` implements `http.Handler`.** `serve.go` already installs
optional capabilities this way three times (`cons.SetSessionAggregator`, `SetPriceTable`,
`SetBodyDecoding`) precisely so a feature bead does not rewrite an existing constructor and its
callers. The new seams carry a price-file path, a retention-day count and a purge surface;
threading those as more positional parameters — or an `Options` struct that rewrites every test
call site — is worse than a return-type change. Store the `*http.ServeMux` on the struct and give
`api` a `ServeHTTP` that delegates to it, so `Handler: api.New(...)` in
[serve.go:110](../../internal/cli/serve.go#L110) still compiles unchanged.

**2. `SetPricing(path string)`.** It carries the price-file **path**, not an in-memory table: the
handler loads and saves through `pricing.Load(path)` / `pricing.Save(path, …)` (D1), so it cannot
accidentally bypass the file the consumer's `Loader` re-reads.

**3. `SetRetention(days int, purge RetentionPurger)`.** The seam carries the configured days *and*
the purge surface, so `api.Store` — documented as "the narrow *read* slice of `*store.Store`"
([api.go:27-45](../../internal/api/api.go#L27-L45)) — stays read-only, and the purge write path
enters through the seam rather than by widening that interface or contradicting its doc comment. An
unwired `SetRetention` is what makes both retention routes answer 503.

`RetentionPurger` declares exactly the store methods the purge and preview handlers call:

```go
PurgeOlderThan(ctx context.Context, cutoff time.Time) (store.PurgeResult, error)
PurgeUnpriced(ctx context.Context) (store.PurgeResult, error)
CountPurgeable(ctx context.Context, cutoff time.Time) (count int, oldest, newest *time.Time, err error)
PurgeableBytes(ctx context.Context, cutoff time.Time) (int64, error)
CountUnpriced(ctx context.Context) (int, error)
UnpricedBytes(ctx context.Context) (int64, error)
```

Two read methods named **for both predicates**, not one pair — the handlers serve two previews over
two different WHERE clauses, so a single `Count*`/`*Bytes` pair cannot produce both the
`eligible_*` and `unpriced_*` numbers. `CountPurgeable` carries the older-than range as
`MIN`/`MAX(started_at)` over the same `started_at < cutoff` rows, because a bare count has no way to
produce the preview's `oldest`/`newest`. Both are **nullable** (`*time.Time`, `nil` when the
eligible count is `0`): `MIN`/`MAX` over an empty eligible set is SQL `NULL` and cannot scan into a
value `time.Time` — the same trap `reconcileSession` avoids by scanning into `sql.NullInt64`
([store.go:917-924](../../internal/store/store.go#L917-L924)).

`(*Store).Vacuum` is deliberately **not** in this interface: `VACUUM` takes an exclusive lock and
blocks ingest for seconds, so it is a CLI-only opt-in and never a dashboard action (R3).

**4. `store.PurgeResult` lands here, in `internal/store/types.go`.**

```go
// store.PurgeResult — exported because it crosses into internal/api.
type PurgeResult struct {
	Deleted            int64 // requests removed
	SessionsReconciled int   // distinct sessions touched
}
```

No json tags: `internal/store` carries none on purpose
([types.go:57-61](../../internal/store/types.go#L57-L61)) and `internal/api` owns the tagged wire
shape.

**Note on bead boundaries — this is the one place a bead splits the plan's outline.** The outline
assigns `PurgeResult` to br-GI-17-05, but this bead's interface declaration is what *forces* the
type into existence, and 05 has no dependency on 02 in either direction. Defining the struct here
and the `purgeWhere` extraction that returns it in 05 keeps both beads independently buildable:
02 lands a type and an interface, 05 lands the methods that satisfy it. br-GI-17-05 must **not**
re-declare `PurgeResult`.

**5. Parameterize `replayOriginReject`'s action name.** The guard's rejection strings are
replay-specific today ("replay requires a loopback Host …", "replay rejected cross-origin request
…", [api.go:604-625](../../internal/api/api.go#L604-L625)). Reusing them verbatim would answer a
rejected `POST /api/purge` or `POST /api/prices` with a body about *replay*, which reads as the
wrong endpoint having been hit — a debugging trap for exactly the person who has just been refused
by a guard they do not understand. Take the action name as a parameter (or return a reason code and
let each handler phrase its own message), and have the mirrored guard test assert the right action
in the body for each route, not merely a 403.

## Rationale

This is the seam bead: it exists so that four later beads each add a handler without touching the
constructor, the guard, or each other. Getting `api.Store` widened instead would be the cheaper
diff today and the wrong one tomorrow — the read-only interface is a real invariant with a real doc
comment, and a purge is not a read.

The guard parameterization is in this bead rather than with the routes because the guard is shared
state: doing it per-route would mean each of the three write routes phrasing the same message
differently.

## Outcome Definition

- `api.New` returns `*api`; `*api` satisfies `http.Handler`; `serve.go` compiles unchanged.
- `newTestAPI` in `api_test.go` returns `*api`, so later beads can reach the seams.
- `SetPricing(path string)` and `SetRetention(days int, purge RetentionPurger)` exist, and an unset
  seam is a supported state rather than a nil dereference.
- `RetentionPurger` is declared with the six methods above, `CountPurgeable` carrying the nullable
  `oldest`/`newest` pair.
- `store.PurgeResult` exists in `internal/store/types.go` with `Deleted` and `SessionsReconciled`,
  and carries no json tags.
- `replayOriginReject` names its action from a parameter; the replay route passes `replay`.
- Every existing `internal/api` test passes unchanged apart from the `newTestAPI` return type.

## Test Specifications

`internal/api` (`api_test.go`):

1. `newTestAPI` returns `*api` and the existing suite compiles against it — the return-type change
   is the whole blast radius, so a green existing suite is the assertion.
2. Guard action table: call `replayOriginReject` (or its replacement) with a non-replay action name
   and assert the returned reason names **that** action, not `replay` — both rejection arms
   (non-loopback `Host`, cross-origin `Origin`).
3. `ServeHTTP` delegates: the existing handler tests that go through `newTestAPI`'s handler still
   pass, which is what proves the mux is stored and reachable.

## Files to Touch

- `internal/api/api.go` (modify — store the mux, `ServeHTTP`, `New` returns `*api`, `SetPricing`,
  `SetRetention` + `RetentionPurger`, parameterize `replayOriginReject`'s action name, update the
  replay call site)
- `internal/api/api_test.go` (modify — `newTestAPI` returns `*api`; the guard action table)
- `internal/store/types.go` (modify — add `PurgeResult`)
