### Bead 6: `feat` cli: wire the calendar through `serve` and repair the call sites

- **Bead ID**: br-GI-24-06
- **Priority**: P0 (critical)
- **Original Estimate**: 1.5h
- **Dependencies**: br-GI-24-02, br-GI-24-03, br-GI-24-04, br-GI-24-05
- **Blocks**: br-GI-24-07, br-GI-24-09

**Description**:

This is the single wiring point, and the first bead whose absence is invisible in a unit test — the zero calendar is silently identical to today's rule, so a forgotten setter makes the whole story do nothing. It also repairs every call site the earlier signature change broke.

**1. Construct the calendar once and install it through the extracted helper** (`internal/cli/serve.go`). Pull the two `SetCalendar` calls out of `Serve` into a named helper, the same "split out from `Serve` so the wiring is testable" pattern `checkRedaction` and `purgeOnStartup` already use (`serve.go:186-217`):

```go
// wireCalendar installs cal on both cold-path seams. Split out from Serve for
// the same reason checkRedaction is: Serve cannot be driven from a test (two
// real listeners, a blocking signal context), so the wiring has to be
// exercised on its own. dash is an interface rather than *api.API because
// api.New returns the unexported *api, which package cli cannot name.
func wireCalendar(cal pricing.Calendar, cons *consumer.Consumer, dash interface{ SetCalendar(pricing.Calendar) }) {
	cons.SetCalendar(cal)
	dash.SetCalendar(cal)
}
```

In `Serve`, construct the calendar from config **once** (after `cfg.Validate()` has already proven it parses) and hand it to all three install points:

```go
cal, err := pricing.NewCalendar(cfg.OffPeakDates, cfg.WorkDates)
if err != nil {
	return fmt.Errorf("serve: build calendar: %w", err) // unreachable: Validate just checked it
}
cons := consumer.New(sk, pubStore, sess, analyze.NewRules(cfg.ModelMap, cfg.ModelMaxTokens, cal))
…
wireCalendar(cal, cons, dashAPI)
```

Note `serve.go:85` is the third install point — the `NewRules` argument is compile-enforced, so it cannot be forgotten; only the two setters can, which is why they go through `wireCalendar`.

**2. Repair every broken `NewRules` call site with the calendar argument.** The full grep-derived set of **8** call sites is: `analyze.go:89` + `analyze_test.go:524/542/555` (owned by br-GI-24-04) and these **4 external** ones, owned here:

- `internal/cli/serve.go:85` — the live wiring above (`cal`).
- `internal/cli/replay_test.go:93` — `analyze.NewRules(cfg.ModelMap, cfg.ModelMaxTokens, pricing.Calendar{})`.
- `internal/replay/replay_test.go:107` — same.
- `internal/consumer/consumer_test.go:91` — the orphaned site (`New(sk, st, nil, analyze.NewRules("", ""))`), which was omitted from v1's plan entirely and is the one br-GI-24-02 explicitly deferred here.

`internal/consumer/consumer_test.go` is region-shared: br-GI-24-02 added the holiday-priced row; this bead edits only the `NewRules` call at `:91`.

**3. This bead restores the full build.** Before it, the tree does not compile outside `internal/analyze` + `internal/pricing` (br-GI-24-04's signature change broke the four sites above). After it, `go build ./...` and `go test ./...` both succeed, and br-GI-24-02's authored-then-deferred holiday-priced consumer test finally runs.

**4. `internal/cli/serve_test.go` is new.** It installs a config-built calendar through `wireCalendar` and asserts it lands on **both** cold-path seams:

- **the API seam**: build a real `api.New(...)`, call `wireCalendar(cal, cons, dashAPI)` and `dashAPI.SetPricing(path)`, then `GET /api/prices` and assert `off_peak_dates` / `work_dates` echo the configured strings (the `SetCalendar` → `DateSets()` path, bead 01/05). A forgotten `dash.SetCalendar` leaves those fields `""` and fails this.
- **the consumer seam**: with the price table installed, insert a call whose `StartedAt` is a configured holiday instant and assert its stored `cost_usd` is the 1× amount (a forgotten `cons.SetCalendar` prices it 2×).

Both assertions are needed: a helper that only wired one seam would pass a one-seam test.

**Rationale**:

D7. Three install points, two of them silent when forgotten. The compile-enforced one (the `NewRules` argument) is the reason br-GI-24-04 changed the signature at all; the two setters are the live risk, and the only way to make a missed setter visible is a test that would fail on it — which is what `wireCalendar` + `serve_test.go` are for. That is the same class of silence GI-4 §4.5 built the `provider_hooks` doctor check to expose.

**Outcome Definition**:

- `serve` builds one calendar from `cfg.OffPeakDates` / `cfg.WorkDates` and installs it on the consumer, the API, and `NewRules`.
- `wireCalendar` exists with the interface third parameter (not `*api.API`, which cannot be named from package `cli`).
- All **4** external `NewRules` call sites pass a calendar; the **8**-site set is complete and `go build ./...` succeeds.
- `go test ./...` passes (including the holiday-priced consumer row authored in br-GI-24-02).
- `serve_test.go` asserts the calendar is in force on both the API and the consumer seams.

**Test Specifications**:

`internal/cli/serve_test.go` (new):

1. **Wiring reaches the API**: build a handler, `wireCalendar(cal, cons, dashAPI)`, `SetPricing`, `GET /api/prices`; assert `off_peak_dates` / `work_dates` equal the configured strings.
2. **Wiring reaches the consumer**: `SetPriceTable` with a fixed rate, `wireCalendar(cal, cons, dashAPI)`, run one captured call at a configured holiday instant; assert the stored `cost_usd` is 1× (and, as the positive control, the same call on an ordinary weekday is 2×). This is the test a missing `cons.SetCalendar` fails.
3. **Unwired is legal**: with no `wireCalendar` call, the API's `/api/prices` date fields are `""` and the consumer prices on the window-and-weekend rule — the supported unset state.

`internal/cli/replay_test.go`, `internal/replay/replay_test.go`, `internal/consumer/consumer_test.go`: mechanical edits only — each `NewRules` call gains a `pricing.Calendar{}` argument; existing assertions unchanged.

**Files to Touch**:
- `internal/cli/serve.go` (modify — build `cal`, `wireCalendar`, `NewRules` third argument at `:85`)
- `internal/cli/serve_test.go` (create — the wiring assertions)
- `internal/cli/replay_test.go` (modify — `NewRules` at `:93`)
- `internal/replay/replay_test.go` (modify — `NewRules` at `:107`)
- `internal/consumer/consumer_test.go` (modify — `NewRules` at `:91` only)
