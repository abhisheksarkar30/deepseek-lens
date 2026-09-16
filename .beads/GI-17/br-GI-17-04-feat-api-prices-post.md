# Bead br-GI-17-04: `POST /api/prices` — set or unset one model's rates

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Contracts §`POST /api/prices`, §Design decisions D3, D4

- **Priority**: P1 (high)
- **Dependencies**: br-GI-17-01, br-GI-17-02, br-GI-17-03
- **Blocks**: br-GI-17-09

## Description

The dashboard's first write route, and the one place the browser can change what the proxy charges.

Body: `{"model":<string>,"rates":{<field>:<number|null>,...}}`, fields from `input`, `output`,
`cache_read`, `cache_write`. It replaces that one model's rates **wholesale**; an omitted field is
unset. Returns the same shape as `GET /api/prices`, so the UI re-renders from what was actually
written rather than from what it sent — which is also what makes R1's lost-update race visible
instead of silent.

The write path is D1's, and it is deliberately three lines: `pricing.Load(path)` → replace one
model's `Rates` → `pricing.Save(path, tbl)`. No in-memory state, no invalidation protocol — the
consumer's `Loader` re-reads on mtime-or-size change
([table.go:243-269](../../internal/pricing/table.go#L243-L269)), so a rate set in the browser prices
the next request with no restart. A naive implementation that mutated only a copy fails the
integration test in br-GI-17-09 and nowhere else.

**Whole row per model, not a field-level PATCH.** This matches how the UI edits ("this row, saved")
and `null` is exactly what `lens prices --unset` means — reverting to a bare model line, which is
what keeps *known but unpriced* distinct from *unknown-model*
([table.go:148-152](../../internal/pricing/table.go#L148-L152)). A field-level PATCH would need a
third syntax to express "unset"; a whole-table PUT would clobber a concurrent `$EDITOR` session.
Adding a model absent from the table is **allowed** — that is how a user fixes `unknown-model`, and
`parseTable` already accepts arbitrary valid model names.

**Status mapping, which is the whole point of br-GI-17-01's sentinels:**

| Status | Cause |
|---|---|
| 400 | invalid model name or negative/NaN/±Inf rate — `errors.Is` against `pricing.ErrInvalidName` / `pricing.ErrInvalidRate` |
| 400 | unknown rate field or malformed body — rejected at decode, before any validation or write |
| 403 | `replayOriginReject` (non-loopback `Host`, or cross-origin) |
| 500 | `Save` failed with anything else — a disk/write error is not a bad request |
| 503 | pricing not wired |

An unknown rate field never reaches `Rates.Set`: decode the body with
`json.Decoder.DisallowUnknownFields()` and `rates` as a struct of the four known `*float64` fields.
That is what makes the contract's "unknown field" a decode-time 400 rather than a `Save`-level 500.
The four fields are pointers so that **omitted** and `null` both mean unset while `0` stays a real,
distinct value.

**Guard.** The route sits behind the same `replayOriginReject` allowlist the replay route uses,
with its action name parameterized by br-GI-17-02. A second write route with a weaker guard is the
failure this story exists to avoid — the listener's entire justification for having no auth is that
it is loopback-bound, and this is one of three write routes now, so its guard test is mirrored from
replay's, both arms.

**Never a GET.** No route in this ticket performs an action on `GET`; setting a rate is a `POST`.

## Rationale

The read route and the write route are separate beads because the write route is the one that needs
br-GI-17-01's typed errors to answer 400-vs-500 correctly, and because a `POST` that persists is a
different risk class from a `GET` that renders. Splitting them also means the guard and the
validation each get their own test file section rather than sharing one crowded one.

## Outcome Definition

- `POST /api/prices` sets a rate; the file on disk carries it, and a subsequent
  `pricing.NewLoader` on that path prices at the new rate.
- `null` unsets a rate (the model reverts to a bare line); an omitted field is likewise unset.
- A model absent from the table is added.
- A rejected name or rate returns `400`, and the price file is **byte-identical** to before.
- A `Save` I/O failure returns `500`, not `400`.
- A cross-origin `Origin` and a non-loopback `Host` each return `403` with a body naming the
  `prices` action, not `replay`.
- The response body has the same shape as `GET /api/prices`.

## Test Specifications

`internal/api` (`prices_test.go`):

1. Set: `POST` a rate for an existing model; assert 200 and that `pricing.Load` on the temp path
   reads the new value back.
2. Unset: `POST` with `"output":null`; assert the field is unset and the other three are untouched.
3. Add: `POST` a model not in the table; assert it appears in the table and in the response.
4. **Rejection leaves the file untouched** — the assertion that separates "refused" from "refused
   after corrupting": capture the file bytes, `POST` a name containing `\n`, assert 400, re-read and
   compare bytes. Repeat for `=`, `.`, a space, and a negative rate.
5. Unknown rate field: `{"rates":{"inpt":1}}` returns 400 and writes nothing (decode-time
   rejection).
6. I/O failure maps to 500, **without depending on how a given OS fails a write**. The tempting
   version — point `SetPricing` at a path under a parent that is a regular file — is GOOS-dependent
   and not guaranteed to reach `Save` at all: the failure may surface in `Load` first, and the
   contract above does not say what a `Load` failure maps to. Instead, extract the mapping into a
   small function and test it directly:

   ```go
   func priceStatus(err error) int // 400 for the two sentinels, 500 otherwise
   ```

   Table-test it with `pricing.ErrInvalidName`, `pricing.ErrInvalidRate`, a `%w`-wrapped sentinel
   (which must still map to 400), and a plain `fmt.Errorf` standing in for a disk error (which must
   map to 500). This is the assertion that fails if every `Save` error is mapped to 400, and it
   holds on every platform. Keep one end-to-end 400 case from the real handler as a sanity check
   that the handler calls the mapping at all.
7. Guard: cross-origin `Origin` → 403; non-loopback `Host` → 403; both bodies name the `prices`
   action.
8. Unwired: without `SetPricing`, 503.
9. Response shape: decode the `POST` response with the same struct `GET` uses, so a shape drift
   between the two routes is a compile error rather than a client bug.
10. **The integration test of D1's premise — automated here, not left to br-GI-17-09's manual
    eyeball.** Build a real `pricing.NewLoader(tempPath)` **before** the write and call `Table()` on
    it, so its stat cache is populated. Then `POST` a new rate. Then call `Table()` again and assert
    the new rate is live.

    Assert through `Loader.Table()`, not `pricing.Load`: `Load` re-reads unconditionally, so a test
    written against it passes even when the file's mtime/size never changed — which is the entire
    mechanism D1 rests on, and the thing br-GI-17-01's atomic rename has to preserve. A naive
    implementation that mutated only an in-memory copy fails here and nowhere else, and so does one
    whose save leaves the mtime/size unchanged.

## Owning the read-only / one-write-route search

This is the first bead to add a write route to `internal/api` beyond replay, so it is the bead whose
change falsifies the "read-only API with one write route" claim, and it owns that governing search
(D7, search 2):

```
rg -n -i -g '!docs/planning/GI-*' -g '!docs/superpowers/specs' \
      -g '!.beads/GI-1' -g '!.beads/GI-4' -g '!.beads/GI-15' \
      -e 'read-only (json )?api' \
      -e '(the|our) (api|dashboard|json api) is read-only' \
      -e 'the one (credentialless )?write (route|guard)' \
      -e 'the one state-changing route' \
      -e 'the only .{0,12}(write|state-changing) route'
```

- **Positive control, asserted before the empty result counts:**
  [broker.go:1](../../internal/api/broker.go#L1) ("the read-only JSON API") must match before the
  change. `rg` is Rust regex — `\|` is a literal pipe and silently matches nothing.
- **Claim-bearing hits in `internal/api` this bead amends:** `api.go:67` (search 2's only `api.go`
  hit), `broker.go`'s package doc, and `api_test.go:83` (the test preamble's "these tests cover the
  read-only API"). `api.go`'s `:78`/`:103`/`:335` wrap the claim across source lines, so line-based
  `rg` does not see them — they are amended as **explicitly named lines**, not as search hits.
- **Claim-bearing hits outside `internal/api`:** `internal/cli/replay.go:207` ("reads one request
  through the read-only API") is search 2's other hit, and this bead amends it alongside the rest of
  the set — it is *not* a declared-stay. The docs half of this search belongs to br-GI-17-11.
- **Declared true lines that stay:** `internal/consumer/analyzer.go:29` and
  `internal/session/session.go:77` ("read-only" aggregates / resolution — true, unrelated to the
  HTTP surface). These two are **not hits of this search** — do not go looking for them in its
  output, and do not treat their absence as a gap. They are named here only so the next reader does
  not "fix" them. Frozen paths stay untouched.

**A note on wording, because the count changes as beads land.** After this bead the listener carries
**two** write routes (replay, prices); br-GI-17-07 adds the third (purge). So this bead rewrites the
claims to be **structurally** honest — the API is no longer read-only, and it names the write routes
that exist — rather than asserting a final count it cannot yet know. br-GI-17-07 updates the count to
three when it lands, and br-GI-17-11 verifies tree-wide that the search's claim-bearing set is empty.

## Files to Touch

- `internal/api/prices.go` (modify — the `POST` handler and its decode/validate/save path)
- `internal/api/prices_test.go` (modify — the cases above)
- `internal/api/api.go` (modify — **one line added to `New`'s route list**:
  `mux.HandleFunc("POST /api/prices", a.setPrices)`; note the method-prefixed pattern, because this
  route must **not** be wrapped in `methodGet`, which rejects every non-GET with a 405. Plus the
  read-only / one-write-route comments at `:67`, `:78`, `:103`, `:335`)
- `internal/api/broker.go` (modify — the package doc's "read-only JSON API")
- `internal/api/api_test.go` (modify — the preamble's "read-only API" claim, `:83`)
- `internal/cli/replay.go` (modify — `:207`, "reads one request through the read-only API")

---

## Review Notes

**The route registration is an `api.go` edit.** As in br-GI-17-03: routes are attached inside `New`
([api.go:83-97](../../internal/api/api.go#L83-L97)), so this bead adds one line there. Note the
method-prefixed pattern — this route must **not** be wrapped in `methodGet`, which rejects every
non-GET with a 405.

**The 500-vs-400 test was platform-dependent and is now a mapping test.** Pointing `SetPricing` at a
path whose parent is a regular file was the tempting way to force a `Save` failure; the failure may
instead surface in `Load`, which the contract does not map, and the errno differs by GOOS. It is
replaced by a direct table test of an extracted `priceStatus(err) int` — sentinel → 400, wrapped
sentinel → 400, plain error → 500 — which is the assertion that actually fails if every `Save` error
is mapped to 400, and holds on every platform.

**The D1 integration test is owned here, not delegated to br-GI-17-09.** It was previously left to
that bead's manual eyeball, which would have left the ticket's central premise — that a browser save
is picked up by the consumer's `Loader` on its next stat — with no automated assertion. It is now
test 10, and it asserts through `Loader.Table()` **after** priming the loader's cache, not through
`pricing.Load`, which re-reads unconditionally and would pass even if the mtime/size never changed.

**The "read-only / one write route" search is owned here** because this is the first bead to add a
write route beyond replay. The bead rewrites the claims **structurally** rather than asserting a
count: after this bead the listener has two write routes, and only after br-GI-17-07 does it have
three. br-GI-17-11 verifies the empty claim-bearing set tree-wide at the end.

