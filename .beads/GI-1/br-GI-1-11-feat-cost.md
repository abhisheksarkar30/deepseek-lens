# Bead br-GI-1-11: Token and cost accounting

**Plan Reference**: `docs/planning/GI-1-deepseek-lens-v1.md` §Bead sequence

- **Priority**: P1 (high)
- **Dependencies**: br-GI-1-04, br-GI-1-07, br-GI-1-08, br-GI-1-09
- **Blocks**: none

## Description

Turn the token counts parsed in br-GI-1-04 into money, honestly labelled.

`internal/pricing` holds a price table: per-model input, output, cache-read, and cache-write rates
per million tokens, keyed by the **upstream-resolved** model (`deepseek-v4-pro`, `deepseek-flash`).

**The table ships with `nil` rates, not guesses.** The spec's open item is explicit: model names and
mapping were verified against DeepSeek's docs; per-token *pricing* was not, and inventing numbers
would produce confidently wrong cost figures. A model with unset rates yields
`Cost{Amount: nil, Source: "unpriced"}` — never `0`, which would read as "this call was free."

`CostSource` is one of:

- `"configured"` — user-supplied rates, arithmetic performed
- `"unpriced"` — rates not configured for this model
- `"unknown-model"` — response model not in the table at all
- `"approximate"` — cache-read tokens priced at the standard input rate because no cache-read rate is
  configured (see the cache-read rule below)

Every surface that shows a cost shows the source alongside it when it is not `"configured"` —
CLI prints `—` (br-GI-1-08's `humanCost` already does this for nil), the dashboard renders a `?` badge
with a tooltip naming the reason. A total that mixes priced and unpriced calls is shown as
`$1.23 + N unpriced`, never silently as `$1.23`.

**`lens prices`** — prints the effective table with sources, and `lens prices --set
deepseek-flash.input=0.28 --set deepseek-flash.output=0.42` writes `~/.deepseek-lens/prices.toml`.
`--edit` opens `$EDITOR`. `--unset <model>` reverts to unpriced. Values are per million tokens, the
unit printed in the header so the number is never ambiguous.

`pricing.Compute(model string, usage parse.Usage, table Table) Cost` is pure and table-driven. It
uses `math/big.Rat` internally for the per-token multiplies so the per-call computation is exact
before any rounding, and it **rounds half-up to whole micro-dollars at the call boundary** — i.e.
rounding happens per call, inside `Compute`, and is not deferred to sum time. `Cost.Amount` is that
rounded micro-dollar value (`nil` when unpriced), and `requests.cost_usd` stores the rounded
per-call amount, so a total over N rows is the exact sum of N integers with no accumulation.
(`ponytail:` big.Rat for money; a decimal library is not warranted for a single multiply-and-sum.)

Cache-read tokens are priced at the cache-read rate **only if** the model has one configured;
otherwise cache-read tokens are priced at the standard input rate and the cost source is downgraded
to `"approximate"`. Given DeepSeek ignores `cache_control` entirely (br-GI-1-10), these fields will
normally be absent — the handling exists so the accounting is not silently wrong if that changes.

Cost is a **separate pre-insert step, not an analyzer**: a wrapper in the consumer calls
`pricing.Compute` and sets `req.CostUSD` / `req.CostSource` on the request before `InsertRequest`.
It returns no `[]store.Warning` and is not registered in the `Analyzer` slice (br-GI-1-07's seam).
`requests.cost_usd` and `requests.cost_source` are thus populated (columns already exist from
br-GI-1-06).

`lens stats` and the dashboard stats view add cost totals with the mixed-pricing rule above, plus
`--by model` and `--by day` breakdowns; `lens export` includes `cost_usd` and `cost_source`.

## Rationale

Token counting already landed in br-GI-1-04; this bead is the part with a correctness trap. The trap is
not arithmetic, it is **presentation**: a tool that reports `$0.00` because it has no price data is
worse than one that reports `unknown`, because the first is silently wrong and the second prompts
the user to fix it. Making `unpriced` a first-class state, propagated to every surface, is the whole
design.

`big.Rat` is used for the multiply-then-sum because agentic sessions produce thousands of calls
whose costs are summed for display; float accumulation over that many tiny values is exactly the
case where rounding drift becomes visible in a total.

## Outcome Definition

- `go test ./internal/pricing/...` passes.
- An unpriced model yields `Amount == nil` and source `"unpriced"` — never zero.
- A configured model computes exact expected cost against hand-calculated fixtures.
- Summing 10,000 small calls yields the exact expected total (drift test).
- `lens prices` prints the table; `--set` writes prices.toml and takes effect without restart.
- `lens prices --unset` reverts to unpriced.
- `lens ls` shows `—` for unpriced calls, never `$0.00`.
- A session total mixing priced and unpriced calls shows the unpriced count alongside the total.
- `lens stats --by model` shows correct per-model totals.

## Test Specifications

- Unit Tests (`internal/pricing/pricing_test.go`):
  - **Unpriced model** → `Amount == nil`, source `"unpriced"`.
  - **Unknown model** (not in table) → source `"unknown-model"`.
  - **Configured**: 1,000,000 input at $0.28/M → exactly $0.28.
  - **Configured mixed**: known input + output → hand-checked exact value.
  - Zero tokens → `$0.00` with source `"configured"` (a real zero, distinct from unpriced).
  - **Drift**: 10,000 calls of 1 input token at **$1.00/M** → each call rounds to exactly 1 micro-
    dollar, total exactly 10,000 µ$ = $0.01, no float error. (The fixture rate is ≥ $0.50/M on
    purpose: at $0.28/M, 1 token = 0.28 µ$ rounds half-up to 0, so every row would store $0.00 and
    the total would be trivially exact — the test would pass vacuously and exercise nothing.)
  - **Rounding** (half-up at the per-call boundary, not truncation): 1 input token at $0.50/M =
    0.5 µ$ → rounds half-up to 1 µ$, not truncated to 0.
  - Cache-read priced at the cache rate when configured.
  - Cache-read with no cache rate → input rate, source `"approximate"`.
  - **Table round trip**: write then read `prices.toml` → identical table.
  - Malformed prices.toml → error naming the offending line, existing table preserved.
  - Negative rate rejected.
- Integration Tests:
  - Proxied call with configured prices → `requests.cost_usd` populated, `cost_source` `"configured"`.
  - Proxied call with no prices → `cost_usd` NULL, `cost_source` `"unpriced"`.
  - `lens prices --set` then `lens ls` → new cost reflected without restarting.
  - `lens stats` total matches the sum of seeded fixture rows.
  - Dashboard `/api/stats` includes cost source breakdown.
- E2E (opt-in): a real request → tokens recorded, cost unpriced until rates are configured.

## Files to Touch

- `internal/pricing/pricing.go` (create)
- `internal/pricing/table.go` (create)
- `internal/pricing/pricing_test.go` (create)
- `internal/cli/prices.go` (create)
- `internal/cli/stats.go` (modify — cost totals and breakdowns)
- `internal/cli/ls.go` (modify — cost column with source)
- `internal/consumer/consumer.go` (modify — wire the pre-insert cost step; not an analyzer)
- `internal/api/api.go` (modify — cost fields in stats responses)
- `internal/web/app.js` (modify — cost column, unpriced badge)
