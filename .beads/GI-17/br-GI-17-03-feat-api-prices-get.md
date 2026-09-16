# Bead br-GI-17-03: `GET /api/prices` — the effective price table

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Contracts §`GET /api/prices`

- **Priority**: P1 (high)
- **Dependencies**: br-GI-17-02
- **Blocks**: br-GI-17-04, br-GI-17-09

## Description

A read-only route serving the effective price table, registered on the seam br-GI-17-02 added.

**Route registration.** Every route in this package is attached inside `New` — `mux :=
http.NewServeMux()` and the whole `mux.HandleFunc` list are one function body
([api.go:83-97](../../internal/api/api.go#L83-L97)), including the `mux.Handle("/",
http.FileServer(...))` catch-all. br-GI-17-02 stores that mux on `*api` but deliberately adds no
`Register` method, so this bead registers its route by adding **one line** to that list:

```go
mux.HandleFunc("/api/prices", methodGet(a.getPrices))
```

That is an edit to `internal/api/api.go`, listed below. Without it the handler is unreachable: the
request falls through to the FileServer catch-all and answers **404**, so every test in this bead
would fail.

```json
{
  "path": "C:\\Users\\me\\.deepseek-lens\\prices.toml",
  "peak_multiplier": 2,
  "models": [
    {"model":"deepseek-flash","input":0.28,"output":1.1,"cache_read":null,"cache_write":null,"source":"configured"},
    {"model":"deepseek-v4-pro","input":null,"output":null,"cache_read":null,"cache_write":null,"source":"unpriced"}
  ]
}
```

The handler resolves the table with `pricing.Load(a.pricePath)` and renders it. No new state, no
cache: the file *is* the interface (D1), so a rate written by `lens prices --set` a second ago is
what this route returns.

Three details that are contract, not presentation:

- **`null` is an unset rate, never an implicit zero.** This is the same distinction `Rates`' pointer
  fields carry ([pricing.go:69-74](../../internal/pricing/pricing.go#L69-L74)) and the same one
  `lens prices --unset` means. A zero would claim the model is free, which is a different and much
  more damaging statement than "no rate configured".
- **`source` is `Rates.Source()`, which returns exactly two values** — `configured` when an input
  rate is set and `unpriced` otherwise ([pricing.go:96-106](../../internal/pricing/pricing.go#L96-L106)).
  It is **not** the same vocabulary as the cost-source buckets: `unknown-model` is a *row* outcome
  from `Compute`, and `SourceApproximate` is a **per-call** outcome (it needs cache-read tokens *and*
  a missing cache-read rate), so `Source()` never reports either. The dashboard renders this verbatim
  and must not re-derive a bucket from the rate values, because a model with all four rates unset
  and a model with only its input rate missing are both `unpriced` here — and the Settings tab must
  not invent a third label the pricing package does not agree with.
- **Models are sorted**, so the table does not reshuffle between renders as the underlying map
  iterates.

`503` when the price path is unwired — the state every `internal/api` test that does not call
`SetPricing` is already in.

**Read-only, and it must stay read-only.** No route in this ticket performs an action on `GET`; this
is the route most tempting to make do so, since it is the one the editor loads from first. `GET`
loads; only `POST` (br-GI-17-04) writes.

`GET /api/prices` discloses the local price-file path. That is deliberate and not new exposure:
`lens prices` and `lens doctor` already print it, and this listener is loopback-only.

## Rationale

The dashboard's pricing surface needs a source of truth that is the file the consumer actually
prices from, not a copy — otherwise the tab can show a rate the proxy is not using, which is the one
failure mode a pricing UI must not have.

## Outcome Definition

- `GET /api/prices` returns `path`, `peak_multiplier`, and a sorted `models` array.
- An unset rate renders `null`; a rate genuinely set to `0` renders `0`.
- `source` is `Rates.Source()` verbatim, and the route never recomputes a bucket from rate values.
- The route answers `503` when no price path is wired.
- The route performs no write: the price file's bytes and mtime are unchanged after a `GET`.

## Test Specifications

`internal/api` (`prices_test.go`):

1. Chart shape: a temp price file with two configured models and one bare-model line; assert
   `path`, `peak_multiplier`, and one entry per model.
2. `null` vs `0`: a model whose rates are unset renders `null` for all four fields; a model with
   `input = 0` renders `0` — asserted separately, because a `float64` zero value and an absent field
   marshal identically and only an explicit `*float64` distinguishes them.
3. `source` is `configured` for a model with an input rate and `unpriced` for a bare-model line; a
   model absent from the table but present in `Default()` is `unpriced` rather than omitted.
4. Sorted output: assert the model order is stable across repeated calls.
5. Unwired: without `SetPricing`, the response is `503`.
6. Read-only: capture the price file's bytes and mtime, issue the `GET`, re-read — unchanged.

## Files to Touch

- `internal/api/prices.go` (create — the `GET` handler and its tagged response struct)
- `internal/api/api.go` (modify — **one line added to `New`'s route list**:
  `mux.HandleFunc("/api/prices", methodGet(a.getPrices))`)
- `internal/api/prices_test.go` (create — the cases above)

---

## Review Notes

**The route registration is an `api.go` edit, not something `prices.go` can do.** This bead first
claimed `prices.go` carried "the route registration"; there is nowhere for it to go. Routes are
attached inside `New`'s body ([api.go:83-97](../../internal/api/api.go#L83-L97)), and br-GI-17-02
adds no registration method. The bead now names the one line it adds and lists `internal/api/api.go`
in its Files to Touch — without it, `GET /api/prices` falls through to the FileServer catch-all and
every test here fails with a 404.

**`Rates.Source()` returns two values, not four.** The bead listed
`configured`/`unpriced`/`unknown-model`/`approximate` as though `Source()` reported them all. It does
not: it is `SourceConfigured` when an input rate is set and `SourceUnpriced` otherwise
([pricing.go:96-106](../../internal/pricing/pricing.go#L96-L106)). `unknown-model` is a *row* outcome
from `Compute`, and `SourceApproximate` is a **per-call** outcome — the latter never appears here
because it needs cache-read tokens *and* a missing cache-read rate. A UI built on the four-value
reading would render a label the server can never send.

