# GI-4 — Peak-aware costing, and hook integration for a proxy-fronted DeepSeek route

**Status**: converged
**Version**: 7
**Issue**: [`GI#4`](https://github.com/abhisheksarkar30/deepseek-lens/issues/4)
**Branch**: `GI-4-peak-cost-and-hook-integration`, cut from `develop`.
**Repos touched**: `deepseek-lens` (this repo) and `agentic-ai-artifacts`
(`D:\github\agentic-ai-artifacts`, the plugin that ships the two hooks).

---

## 1. Problem

Two independent components encode the same fact — *DeepSeek is the provider, and DeepSeek bills
2x during 01:00–04:00 and 06:00–10:00 UTC, Mon–Fri* — and neither knows about the other.

**The plugin's hooks are blind to a lens-fronted session.** `deepseek-peak-guard.sh:14` and
`deepseek-auto-toggle.js:72` both decide "is this session DeepSeek-backed?" by asking whether
`ANTHROPIC_BASE_URL` contains the substring `deepseek`. Put lens in the path and that value
becomes `http://127.0.0.1:8787`, which contains nothing of the sort. The consequences differ by
hook and both are wrong:

- **Peak guard**: `exit 0` — the guard steps aside and **never blocks**. The exact billing event
  it exists to prevent (a 2x-peak session) proceeds unguarded. Silent.
- **Auto-toggle**, off-peak: reads `current = "pro"`, sees the overlay is not what it wants, and
  installs the direct-DeepSeek overlay *over* `settings.env` — **overwriting `ANTHROPIC_BASE_URL`
  and dropping lens out of the path on the next launch**. Observability stops, silently.
- **Auto-toggle**, during peak: reads `current = "pro" == desired`, returns early, and does
  nothing — so the session keeps billing DeepSeek at 2x through the window the toggle exists to
  close.

**What the fix reaches — and what it does not.** §4.1 makes both hooks recognize a lens-fronted
route *when the overlay `~/.claude/.deepseek-env.json` declares the proxy's URL*. In that
configuration the peak guard blocks and the toggle leaves lens in place. It does **not**, on its
own, repair the off-peak "drops lens" case above: there the overlay still declares the *direct*
DeepSeek URL while `settings.json` points at lens, so the toggle keeps overwriting the lens URL
until the user edits the overlay — a setup change bead 08 documents, not a code change. The
predicate cannot be widened to cover that case without guessing, because a hook has no way to learn
the address lens is configured to serve; only the overlay can declare that the route is a proxy.
§8.2 records this limit; it is restated here because §1 is where a reader first meets the failure.

**lens prices every peak call at half its real cost.** lens is DeepSeek-only by construction
(`config.Default().UpstreamURL` is `https://api.deepseek.com/anthropic`; the shipped model map
resolves every client model to `deepseek-v4-pro`/`deepseek-flash`), yet `pricing.Compute`
(`internal/pricing/pricing.go:105`) multiplies tokens by a configured rate with **no time input
at all**. 01:00–04:00 and 06:00–10:00 UTC Mon–Fri is 35 of 168 hours — **~21% of the week** in
which every `cost_usd` this tool reports is understated by exactly 2x. In a tool whose stated
thesis is that a number is never invented (`CostSource` exists for that reason, and the shipped
table carries no rates rather than a guessed one), a silently halved total is the specific
failure the design set out to avoid.

The two halves are one problem: the plugin is the component that *prevents* peak billing, and
lens is the component that *measures* it. Today the plugin fails to fire against lens, and lens
would misreport the spend even if it did.

## 2. Scope

**lens works, and is verified to work, with no plugin at all.** That is a requirement of this
story, not an assumption resting on the reader: the two hooks are an *optional* integration, and
a user who runs no plugin — a Cline user, a plain `curl` user, or anyone whose config directory
holds no Claude Code files — must get a fully working lens and a successful `lens doctor`. Every
lens path except the `provider_hooks` check in §4.5 reads nothing the plugin owns (§3); the check
itself must therefore be incapable of failing, and is specified that way.

In scope:

1. Both plugin hooks recognize a DeepSeek route that is fronted by a local proxy.
2. lens knows the peak window, prices peak calls correctly, and names the calls it priced at
   peak.
3. `lens doctor` reports whether the plugin hooks will recognize the current route (the
   integration's failure mode is silence, so it needs a surface that says so out loud), and
   degrades to a passing note when there is no plugin to report on.
4. User-facing docs on both sides, including that the plugin integration is optional.
5. Both hooks locate Claude Code's config directory the way Claude Code does — honoring
   `CLAUDE_CONFIG_DIR` when it is set, and otherwise resolving `~/.claude` by the platform's own
   home rule (`%USERPROFILE%\.claude` on Windows, `$HOME/.claude` on Unix).

Explicitly out of scope:

- **Nothing in this story may make lens's correctness, its ability to start, or its `doctor` exit
  code depend on a plugin-owned file.** This is the constraint the rest of the design is written
  against, and it is the reason §4.5 is specified case-by-case rather than left to the
  implementer's judgement.
- No new `CostSource` value. See §4.3 for why.
- No new lens configuration knob. See §4.2.
- No change to lens's blocking behaviour: lens observes, it never refuses a request. The guard
  blocks; lens measures.
- No attempt to share the peak window as code between the Go and JS/bash implementations — the
  repos cannot import from each other. See risk §8.1.
- No rewrite of the plugin's documented setup around a relocated config directory: the manual
  steps in its README (copying `deepseek-key.ps1`, the absolute `apiKeyHelper` path inside the
  overlay) stay user-declared absolute paths. See §8.6.

## 3. Verified current state

Every claim below was read out of source, not taken from `docs/context/`. Where a context doc
would now be stale, it is listed in §7 for the Phase 6.5 refresh.

| Claim | Evidence |
|---|---|
| Both hooks key off `ANTHROPIC_BASE_URL` containing `deepseek` | `deepseek-peak-guard.sh:14-17`, `deepseek-auto-toggle.js:72-76` |
| Neither hook honors `CLAUDE_CONFIG_DIR`; each builds `~/.claude` from its host language's home helper — Node's `os.homedir()` (which resolves `USERPROFILE` first on Windows), bash's `${HOME:-$USERPROFILE}` | `deepseek-auto-toggle.js:27-30`, `deepseek-peak-guard.sh:19` |
| The overlay `~/.claude/.deepseek-env.json` declares the route the toggle owns, in either a legacy flat shape or an `env`/`settings` two-section shape | `deepseek-auto-toggle.js:87-100`; both shapes exercised by `hooks/test-deepseek-auto-toggle.sh` |
| `pricing.Compute` takes no time input | `internal/pricing/pricing.go:105` |
| `Compute` has exactly one production caller | `internal/consumer/consumer.go:382` |
| `store.Request.StartedAt` is a `time.Time`, set on every row before the cost step | `internal/store/types.go:12`, `consumer.go:317`, cost step at `consumer.go:381` |
| Warning rules receive the whole `*store.Request`, so a rule can read `StartedAt` with no signature change | `internal/analyze/rules.go:15-20`; `ruleUpstreamError` already reads `in.req.RespBody` |
| A new warning kind requires a matching README row, enforced by test | `internal/analyze/readme_test.go` — equality, not containment, between `AllKinds()` and the table between the `BEGIN/END warning kinds` markers |
| The README warning table is not limited to "dropped parameters" — it already carries `upstream_error` and `analyzer_panic` | `README.md:92-104` |
| The dashboard's cost badge assumes any non-`configured` source means "no configured price" | `internal/web/app.js:35-36` |
| `cost_source` is documented as one of exactly four values | `docs/context/data-model.md:72` |
| lens's own conventions: `GI#<n>` prefix, `GI-<n>-<slug>` branch, `docs/planning/GI-<n>-<slug>.md` plan, `.beads/GI-<n>/br-GI-<n>-<NN>-<type>-<slug>.md` beads | `CLAUDE.md` |
| Issue #4 is OPEN and is this story | `gh issue list --state all` |

## 4. Design

### 4.1 The hooks: ask the honest question

The root cause is one predicate, wrong in both files, for the same reason: *"does the base URL
contain `deepseek`?"* is a proxy for *"is this session DeepSeek-backed?"*, and the proxy breaks
the moment something else sits in front of DeepSeek.

**Fix the predicate, in both files, to the question it was standing in for:**

> A session is DeepSeek-backed when its effective `ANTHROPIC_BASE_URL` either names DeepSeek, or
> is exactly the base URL that `~/.claude/.deepseek-env.json` declares.

The second clause is the load-bearing one, and it is the same file the toggle already treats as
the declaration of what it owns. That file *is* the machine's statement of what the DeepSeek
route looks like — for a direct setup it names `api.deepseek.com`; for a lens-fronted setup the
user points it at the lens proxy. Keeping the substring test as the first clause means a machine
with a hand-set DeepSeek URL and no overlay still behaves exactly as it does today.

This is a deliberate improvement over "treat any loopback URL as DeepSeek": that would guess, and
it would guess wrong on the first machine that puts some *other* local proxy in front of the
Anthropic API — and the peak guard's failure mode for a wrong guess is refusing the user's work.

`deepseek-auto-toggle.js` already parses the overlay (into `ownedEnv`, normalized across both
shapes) — the predicate reuses that, so the only change is *ordering* (read the overlay before
computing `current`) plus the predicate itself.

`deepseek-peak-guard.sh` has no JSON parser and must not grow one. It needs one string out of a
small, hand-written file, so a targeted `grep -o` on the single `ANTHROPIC_BASE_URL` key is
sufficient, and it is shape-agnostic (the key is spelled the same in the legacy flat shape and
the sectioned shape). This mirrors the repo's existing taste for a tiny hand-rolled reader over a
dependency — `internal/config`'s `parseFlatFile` carries the same justification.

One divergence follows from having no resolver: the guard greps the *raw* key, so an overlay
declaring `ANTHROPIC_BASE_URL: "${LENS_URL}"` is recognized by the toggle (which resolves `${VAR}`
before comparing) and **not** by the guard, which compares the literal `${LENS_URL}` against the
resolved `$ANTHROPIC_BASE_URL` and takes its `*) exit 0` branch. The two files implement the same
*rule*, not the same code. That placeholder handling is the one place they currently diverge; it is
a **known gap**, not a regression — its failure direction is "not recognized", today's behaviour —
and it is recorded with an upgrade path in §8.4.

Both files get a cross-reference comment naming the other, because the *drift between these two
copies is the bug being fixed*. That coupling is also the top risk (§8.1).

### 4.2 lens: the peak window and the multiplier

Two exported additions to `internal/pricing`, which is already the leaf package that owns money:

```go
// PeakMultiplier is DeepSeek's peak/off-peak price ratio. Not user-configurable ...
const PeakMultiplier = 2.0

func IsPeak(t time.Time) bool   // 01:00-04:00 or 06:00-10:00 UTC, Mon-Fri
```

**The multiplier is a constant, not a config knob.** lens makes *rates* configurable because the
shipped table must not invent a number that varies per model and per DeepSeek's pricing page. The
peak ratio is a single documented global — today's plugin already hardcodes `2x` in its own
blocked-prompt message — and adding `LENS_PEAK_MULTIPLIER` would buy a config key, a `NewRules`
parameter and eight call-site updates to serve a number nobody has a reason to change. It is
marked with a `ponytail:` comment naming the upgrade path (move to config) for the day DeepSeek
moves it.

`Compute` gains the call's start time and applies the multiplier to the **rates, before
summation**, as an exact `big.Rat` multiply:

```go
func Compute(model string, usage parse.Usage, table Table, at time.Time) Cost
```

Multiplying rates rather than the total keeps the package's documented "rounding happens once
per call, here" property intact and exact rather than approximately exact — doubling a rate is
exact in `big.Rat`, whereas doubling a rounded total is a second rounding site.

The alternative — a `ForTime(table, at) Table` transform leaving `Compute`'s signature alone —
was rejected. It reads cleaner and touches no test, but `pricing.Loader.Table()` returns its
*cached* table by reference, so a transform that mutated in place would permanently double the
rates for every later call, off-peak included. Introducing an aliasing hazard into the money path
to avoid eleven mechanical edits is the wrong trade.

**No new configuration.** No `LENS_*` variable, no `config.toml` key, no `config.Config` field.
The window is a constant in the package that prices; the multiplier likewise.

### 4.3 lens: how peak becomes visible

The corrected `cost_usd` fixes the totals. It does not, on its own, explain *why* a call cost more
than the same call cost an hour ago, and it does not give the user the retroactive signal that
part of their spend was avoidable — which is the entire point of a peak guard.

That signal goes in the **warning table**, as a new kind `peak_pricing` (severity `warn`), not in
`cost_source`:

- A fifth `cost_source` value would make the dashboard **lie**. `internal/web/app.js:35-36`
  renders any source other than `configured` as *"no configured price for this call (lens prices
  --set)"* — a call priced at peak *is* priced, so that tooltip would be false on exactly the
  rows the change creates. Avoiding that means editing badge logic in JavaScript, where this repo
  has no tests.
- The warning table is already mechanically honest: `readme_test.go` enforces *equality* between
  `AllKinds()` and the README rows, so a kind cannot be added without its user-facing sentence.
- The table already carries kinds that are not "a dropped parameter" (`upstream_error`,
  `analyzer_panic`), so a billing fact is not a category error there.

The rule fires only when the call was **actually priced and carried spend**
(`req.CostUSD != nil && *req.CostUSD > 0`). A `nil` means no number at all (an unpriced call), and
a configured model with zero tokens prices to a real `0` — non-nil, but no spend. A warning
claiming either was "billed at peak" would assert something lens does not know, the same
discipline as `SourceUnpriced` existing rather than a zero.

**For a lens user with no plugin, this warning is not a supplement — it is the only peak signal
they will ever get.** A plugin user has a guard that refuses the session *before* it bills, so for
them the warning is a retrospective explanation of a call the guard did not stop. A user running
no plugin has no guard and no toggle, and nothing else in lens knows that 02:00 UTC costs double:
the warning is the entire mechanism by which they learn that part of their spend was avoidable,
and the doubled `cost_usd` is only interpretable once they can see it. That asymmetry is why bead
03 is P0 rather than P1 — for the half of this story's audience that runs no plugin, the warning
*is* the feature.

`cost_source` stays `configured` on a peak-priced call, and that is correct rather than a
compromise: the arithmetic was performed with the user's own rates. `CostSource` describes the
provenance of the number, not the calendar.

### 4.4 The rule needs no plumbing

Rules receive `ruleInput{meta, usage, req, opts}` — the whole row. `req.StartedAt` is already set
before the cost step and is the same value the cost step prices against, so `rulePeakPricing`
reads `in.req.StartedAt` and calls `pricing.IsPeak`. **`NewRules` does not change**, and neither
does any of its eight call sites. Nothing carries a "peak window" through the consumer.

This is why `internal/analyze` importing `internal/pricing` is acceptable: `pricing` imports only
`parse`, so no cycle forms, and CLAUDE.md's dependency invariant (which pins `proxy`, and only
`proxy`) is untouched.

### 4.5 `lens doctor`: make the silence visible, and never fail without the plugin

The hooks' failure mode is silence — the guard simply does not fire and nothing says so. A doctor
check that evaluates the **hook predicate** against the effective `ANTHROPIC_BASE_URL` read from
Claude Code's settings file — substring on `deepseek` first, else equality with the URL the
overlay declares — reports whether the peak guard will recognize this route, turning the silence
into a printed line. It is the **third copy of the predicate** (§8.1), so it is written as the
predicate itself, not an approximation of it: a direct-DeepSeek route passes by the substring
clause however the overlay reads, and the only warning is a lens-fronted route whose **present**
overlay declares a *different* address — an absent overlay passes with a note.

**This check is the only lens code that touches a plugin-owned file, and therefore the only place
this story could make lens fail without the plugin.** `runDoctor` returns a non-zero exit as soon
as one check returns `FAIL` (`internal/cli/doctor.go:41-45`), so the check must be incapable of
returning `FAIL`: every unexpected condition — no config directory at all, no `settings.json`, an
unreadable file, a directory where a file was expected, an unparseable overlay — resolves to
`pass` with a note. `WARN` is reserved for the one genuinely actionable state (a present overlay
declaring a different address), and even that must not fail the command. Bead 04 enumerates the
cases and tests each.

**Resolving Claude Code's config directory is a different question from resolving lens's home, and
must not reuse the helper built for the latter.** Claude Code relocates its whole config directory
— `settings.json`, session history, plugins together — when `CLAUDE_CONFIG_DIR` is set, and on
Windows `~/.claude` is documented as meaning `%USERPROFILE%\.claude`, not `$HOME/.claude`.
`config.userHomeDir()` (`internal/config/config.go:83`) resolves `$HOME` **first**, deliberately,
so that tests and users can redirect *lens's own* files (`config.toml`, `prices.toml`, the
database) uniformly across platforms. Inheriting that precedence here would read a directory
Claude Code does not use whenever `$HOME` differs from `%USERPROFILE%` — the ordinary state of a
Git Bash session on Windows, this repo's target platform — and the check would then answer
confidently about a file the hooks never read. So bead 04 adds a small `claudeConfigDir()` beside
its only caller, honoring Claude Code's precedence: `CLAUDE_CONFIG_DIR`, else `%USERPROFILE%` on
Windows, else `$HOME`, else `os.UserHomeDir()`. That last chain is also exactly what Node's
`os.homedir()` resolves in the hooks, so the check finally reads the same file the hooks do.
`config.UserHomeDir` is therefore **not** exported, and `internal/config/config.go` is not touched
by this story.

P2, and the one bead in this plan that could be dropped without weakening the fix — but it is
also the bead that *proves* the no-plugin guarantee, since it is the only place that guarantee can
be broken.

## 5. Data flow

Unchanged in shape; one new input joins the fixed cold-path order without reordering it.

```
client -> proxy -> (tee) -> sink -> consumer
                                     |
                                     +- ExtractMeta / ExtractUsage
                                     +- resolve session
                                     +- cost step:  pricing.Compute(model, usage, table, req.StartedAt)
                                     |                 ^ still before InsertRequest: cost_usd/cost_source are columns
                                     +- InsertRequest
                                     +- analyzers:  ... rulePeakPricing reads req.StartedAt
```

The hot path is untouched: no new per-request work, no allocation on the proxy listener. The
cost change is on the cold path, where a `big.Rat` multiply per priced call is free relative to
the SQLite write beside it.

## 6. Changes

### `deepseek-lens`

| File | Change |
|---|---|
| `internal/pricing/pricing.go` | add `IsPeak`, `PeakMultiplier`; thread `at time.Time` through `Compute` and apply the multiplier to each rate as a `big.Rat` |
| `internal/pricing/pricing_test.go` | update 11 `Compute` call sites to an off-peak time; add window-boundary, weekend, UTC-not-local, and exact-2x cases |
| `internal/pricing/table.go` | the `Save` header comment block (off-peak note) (bead 05) |
| `internal/analyze/kinds.go` | add `KindPeakPricing` and its one-sentence description |
| `internal/analyze/rules.go` | add `rulePeakPricing` to the rule table |
| `internal/analyze/analyze_test.go` | add cases including peak+priced, peak+unpriced, and off-peak (six in all — see bead 03) |
| `README.md` | add the `peak_pricing` row inside the `BEGIN/END warning kinds` markers; note peak pricing in the cost section, and that the plugin integration is optional |
| `internal/consumer/consumer.go` | pass `req.StartedAt` to `Compute` (one line) |
| `internal/consumer/consumer_test.go` | pin `simpleCall()`'s `StartedAt` off-peak (shared fixture); peak/off-peak row assertion (bead 02) |
| `internal/cli/doctor.go` | hook-recognition check (P2), plus the `claudeConfigDir()` resolver it reads through — `CLAUDE_CONFIG_DIR`, else `%USERPROFILE%`, else `$HOME` |
| `internal/cli/cli_test.go` | the check's cases, including every no-plugin path (absent config dir, absent or unreadable `settings.json`) |
| `docs/context/data-model.md` | add `peak_pricing` to the warning-kind list |
| `docs/context/*.md` | Phase 6.5 refresh |

### `agentic-ai-artifacts`

| File | Change |
|---|---|
| `hooks/deepseek-auto-toggle.js` | resolve the config directory (`CLAUDE_CONFIG_DIR` first); read the overlay before computing `current`; add the shared predicate; cross-reference comment |
| `hooks/deepseek-peak-guard.sh` | resolve the config directory the same way; replace the substring guard with the shared predicate; cross-reference comment |
| `hooks/test-deepseek-auto-toggle.sh` | pin the fixture's config directory through `CLAUDE_CONFIG_DIR`; add lens-URL fixtures on both sides of the peak window |
| `README.md` | document the proxy-fronted overlay, the hook-reinstall requirement, and the relocated-config-directory caveat (§8.6) |

## 7. Test strategy

**Unit — `internal/pricing`** (the money path; the highest-value tests here):
- `IsPeak` at every boundary: 00:59, 01:00, 03:59, 04:00, 05:59, 06:00, 09:59, 10:00 — each on a
  weekday.
- `IsPeak` false on Saturday and Sunday at 02:00 and 07:00, both windows covered.
- `IsPeak` classifies by UTC regardless of the `time.Time`'s location — the same instant
  expressed in two zones gives the same answer. This is the one that fails silently if someone
  writes `t.Hour()`.
- Identical usage, in-window vs out-of-window, returns exactly `PeakMultiplier`-scaled
  micro-dollars (2x the off-peak amount) — an integer equality, not a tolerance.
- Round-half-up still happens once per call: a usage that is unpriced stays `nil` at peak.

**Unit — `internal/analyze`**: six cases in all (see bead 03) — including that `peak_pricing`
fires on a peak-priced call, does not fire on a peak call whose `CostUSD` is nil, and does not fire
off-peak. `readme_test.go` covers the README row by construction once the kind is registered.

**Unit — `internal/consumer`**: the cost step passes the call's own `StartedAt` (a fixture with a
peak timestamp yields a doubled `cost_usd` on the row). The shared `simpleCall()` fixture
(`internal/consumer/consumer_test.go:38`) is pinned to a fixed off-peak instant instead of
`time.Now()`, so the exact-cost assertions that inherit it (`pricedCall` at `:832`, asserting
`0.28` at `:884`/`:961` and `0.31` at `:975`) stay green at any wall-clock hour; a
`time.Now()`-derived `StartedAt` is the specific trap the pin closes. Because the pin gives every
`simpleCall()`-derived row the *same* timestamp, `TestCostStepTakesEffectWithoutRestart` (`:942`,
asserting `rows[0]` at `:975`) must stamp its second call with a **distinct** off-peak `StartedAt`
(e.g. base + 1 hour) — otherwise `rows[0]`'s ordering would rest on the `idx_requests_started_at`
tiebreak (`store.go:341` orders by `started_at DESC` with no secondary key; `schema.sql:45`)
rather than on the fixture. Both rows stay off-peak, so no multiplier applies.

**Unit — `internal/cli`** (bead 04; this is where the no-plugin guarantee is verified): every
condition that arises when there is no plugin, or no Claude Code at all, resolves to `pass` and
leaves `doctor`'s exit code at zero — no config directory, an empty config directory with no
`settings.json`, an unreadable `settings.json`, a directory where the file was expected, a
malformed overlay, a sectioned overlay carrying the same URL under `env`. The four predicate
combinations each exercise both clauses. Tests point at a throwaway config directory through
`CLAUDE_CONFIG_DIR`, which isolates on every platform in one line; a test that set only `$HOME`
would read the developer's real `~/.claude/` on Windows, since the resolver prefers
`%USERPROFILE%` — the leak the isolation exists to prevent.

**Hook tests** — extend `hooks/test-deepseek-auto-toggle.sh`, which already runs the toggle
against a throwaway `HOME` with a pinned clock (this is the pattern to follow, not a new harness):
- lens URL in the overlay, off-peak → the overlay is left **exactly** as it is (today: it is
  needlessly rewritten — the toggle merges the same lens URL back over `settings.env`
  (`deepseek-auto-toggle.js:136`), clobbering a hand-rotated key; lens itself stays in the path).
  The config that genuinely *drops* lens is the inverse one — overlay declaring the direct URL
  while `settings.json` points at lens — which §1 says bead 07 does not repair; bead 08 documents
  that setup (§8.2).
- lens URL in the overlay, peak → the overlay is removed (today: nothing happens).
- A new guard case covering a lens URL during peak, asserting the block, and one off-peak
  asserting silence.
- **The harness pins `CLAUDE_CONFIG_DIR` to the throwaway dir.** It currently redirects through
  `USERPROFILE="$tmp" HOME="$tmp"` on every invocation, which stops working the moment the hooks
  honor `CLAUDE_CONFIG_DIR`: a developer who has that variable set would have every case read and
  write their real config directory. One exported assignment next to `tmp="$(mktemp -d)"` covers
  every child process, including the `bash "$guard"` calls.
- **A relocated-config-directory case**: with the fixture reachable only through
  `CLAUDE_CONFIG_DIR` and the platform home variables pointing elsewhere, both hooks still find
  the overlay and the settings file. The toggle half is asserted on both sides of the window:
  off-peak it leaves the lens URL in place *and* emits no `couldn't read` systemMessage (a
  fold-in-less toggle reads the missing home directory, prints that message at
  `deepseek-auto-toggle.js:62-70` and returns without writing, so the no-write assertion alone
  would pass — the absent-message assertion is what makes the case fail), and at peak it removes
  the overlay and the owned `settings` keys (a fold-in-less toggle removes nothing). The guard
  half blocks at peak. This is the case that pins the fold-in, and it now fails against either
  hook's pre-bead-07 code: the toggle's pre-change run reads the missing home directory and acts
  on nothing, the guard's pre-change run has no overlay read to match the lens URL against.

**Integration / E2E**: deferred to `/develop-tests`. The natural one is the end-to-end claim this
plan makes — a session routed through lens during a pinned peak window records a `cost_usd`
exactly double the same call off-peak, with a `peak_pricing` warning attached.

**What is deliberately not tested**: the two hook files against each other. There is no shared
harness for them, and building one is a larger change than the bug it would guard (§8.1).

## 8. Risks

**8.1 The peak window now exists in three places** — `deepseek-peak-guard.sh`,
`deepseek-auto-toggle.js`, and `internal/pricing.IsPeak` — in three languages, across two repos
that cannot import from each other. They will drift, and a drifted window means either lens
mispricing a window or the guard blocking outside one. The three copies are **unguarded against
drift**: no test in either repo executes the other implementation, so a shared date literal detects
nothing cross-repo — moving Go's boundary fails Go's own boundary test whether or not the JS/bash
tests use the same date. What actually works is each side's own boundary test: lens pins `IsPeak`
at the 01:00/04:00/06:00/10:00 edges and the Fri→Sat transition; the plugin pins its own
`FRI_PEAK`/`FRI_OFF`/weekend cases. *Mitigation*: each of the three carries a comment naming the
other two and the window's source, and their boundary tests share the *same* fixture dates so the
two suites read **analogously**, not because sharing the dates detects a one-sided change. This is
a real limit of the design, not a solved problem — recording it here so it is a known cost. The
**recognition predicate** is now a **second** three-copy surface — `deepseek-peak-guard.sh`,
`deepseek-auto-toggle.js`, and the `provider_hooks` check bead 04 adds — three implementations across
the same two repos with no cross-check, the same structural drift risk as the window copies above.

**8.2 The fix is inert unless the overlay declares the proxy URL.** A user who points
`ANTHROPIC_BASE_URL` at lens by hand, while the overlay still declares `api.deepseek.com`, gets
no recognition. *Mitigation*: the plugin README documents the setup (bead 08), and `lens doctor`
reports the mismatch out loud (bead 04).

**8.3 `Compute`'s signature changes in the money path.** *Mitigation*: every existing pricing
test is updated to an off-peak time, so all eleven keep asserting exactly what they asserted
before; the zero `time.Time` (Jan 1, year 1, 00:00 UTC — a Monday, hour 0) is off-peak, so the
mechanical edit cannot accidentally turn a test into a peak case. The consumer integration
fixtures are the other half: `simpleCall()` (`internal/consumer/consumer_test.go:38`) stamps
`StartedAt: time.Now()`, so once the cost step prices a row at the row's own timestamp, the
exact-cost assertions that inherit it (`:884`, `:961`, `:975`) would fail *during the peak window
itself*. Bead 02 pins that shared fixture to a fixed off-peak instant, and the suite must be green
both inside and outside the peak window.

**8.4 The bash overlay extraction is a targeted `grep`, not a parser.** A future overlay shape
that spells the key differently, or an `ANTHROPIC_BASE_URL` appearing in some other context
inside the file, would break it. *Known gap — `${VAR}` placeholders*: the JS predicate resolves
`${VAR}` before comparing, but the guard has no resolver and greps the raw key, so an overlay
declaring `ANTHROPIC_BASE_URL: "${LENS_URL}"` is recognized by the toggle and silently missed by
the guard. Its failure direction is "not recognized" (today's behaviour), so it is never a
regression, but it is a real divergence between the two implementations of the same rule.
*Mitigation*: the predicate's failure direction is "not recognized" — a strict improvement, never
a regression — and bead 07's tests cover both overlay shapes the JS hook already supports, plus
the placeholder boundary. *Upgrade path*: resolve the placeholder in bash too, or make the guard
delegate to a shared resolver the JS also calls (which would also retire the shape-drift risk
above).

**8.5 A false-positive guard blocks the user's work.** The equality test on the declared URL was
chosen over a loopback heuristic precisely to keep this rare; `.provider-override` remains the
documented escape hatch and is untouched.

**8.6 Claude Code's config directory is not always `~/.claude`.** `CLAUDE_CONFIG_DIR` relocates
Claude Code's config directory — settings, session history and plugins together — and on Windows
`~/.claude` is documented as meaning `%USERPROFILE%\.claude` rather than `$HOME/.claude`. Both
sides of this story had the same defect, and both are fixed in it.

**lens's side** (bead 04): `provider_hooks` resolves through a `claudeConfigDir()` (§4.5), so it
reads the file the hooks read rather than the one the Go helper happened to prefer.

**The plugin's side** (bead 07): `deepseek-auto-toggle.js:28` hardcoded
`path.join(home, ".claude", "settings.json")`, so for a user with a relocated config directory the
hook edited a file Claude Code does not read — it appeared to act, and had no effect. The guard
had the same class of defect: it built its `~/.claude` path from `${HOME:-$USERPROFILE}` (`:19`),
which prefers `HOME` and so, on Windows, reads a directory Claude Code never uses. Both hooks now
resolve `CLAUDE_CONFIG_DIR` first, then Claude Code's own home rule — `os.homedir()` in the JS
(`USERPROFILE` on Windows, `HOME` elsewhere), `${USERPROFILE:-$HOME}` in the guard — so on the
unset path both agree with Claude Code and with each other. *Not a regression*: unsetting
`CLAUDE_CONFIG_DIR` changes nothing wherever the platform home and `HOME` agree — all of Unix, and
Windows where `HOME` is unset or already equals `%USERPROFILE%`. Where they diverge on Windows the
guard's path *is* deliberately changed — from the `$HOME` directory Claude Code ignores to
`%USERPROFILE%\.claude`, the one it reads — which is the point of the fix. *Residual boundary*: the
plugin's README documents manual setup steps (copying `deepseek-key.ps1` to `~/.claude/`, an
absolute `apiKeyHelper` path inside the overlay) that remain user-declared literals; a user who
relocates their config directory must adjust those themselves, and bead 08 says so where it
documents the setup.

**8.7 Self-review lens** (required by the flywheel): *security* — no auth, secret, or permission
surface is touched; the hooks read a local config file that already holds no credential (the key
lives in a user environment variable, read by `apiKeyHelper`); no new network call is added
anywhere; lens's loopback-only binding and `x-api-key` redaction are untouched. *QA* — the
edge cases that matter are the window boundaries, the UTC-vs-local trap, the unpriced-at-peak
case, the overlay-shape variants, and the no-plugin conditions of §8.6 and §4.5, all enumerated
in §7. *Architecture* — the cost step keeps its place in the fixed pipeline order, the hot path
gains nothing, no new configuration or dependency is introduced, and the one new read of a
foreign file is confined to a check that cannot fail the command it runs under.

## 9. Pre-flight (needs a decision before bead 01)

1. **Cut the feature branch.** `GI-4-peak-cost-and-hook-integration`, from `develop`. This repo's
   governance keys everything off the issue: the branch is `GI-<n>-<slug>`, every non-merge commit
   is prefixed `GI#<n>`, and `branch-guard.yml` rejects a PR whose title does not name a real
   issue — which is why the plan and every bead are written against `GI#4`.

   *(Note for the next story: GitHub numbers issues and PRs in one sequence. The next free number
   is therefore not "highest issue + 1" — this plan was first drafted against `GI#2` and had to be
   renumbered to `GI#4` after checking `gh issue list --state all` against the PR numbers.)*
2. **Hook changes need a plugin reinstall to take effect.** Per the plugin's own README, `hooks/`
   ships in the bundle — `/plugin marketplace update agentic-ai-artifacts`, then uninstall +
   install (there is no `--force`). Committing the plugin changes is not enough to arm them.
3. **Two-repo commits.** Bead 07/08 change code in `D:\github\agentic-ai-artifacts`, which is a
   different repository with its own conventions (no `GI#` prefix — its history is plain
   conventional commits). Their bead files still live in this repo's `.beads/GI-4/` as the audit
   trail, but the code commits land in the other repo's history.

## 10. Beads

| # | Title | Priority | Depends | Repo |
|---|---|---|---|---|
| 01 | `feat` pricing: peak-window predicate and multiplier | P0 | — | lens |
| 02 | `feat` pricing: apply the peak multiplier in Compute | P0 | 01 | lens |
| 03 | `feat` analyze: `peak_pricing` warning kind and rule | P0 | 01 | lens |
| 04 | `feat` cli: doctor reports whether the hooks recognize the route | P2 | 07 | lens |
| 05 | `docs` lens: document peak pricing in README and the price-file header | P2 | 02, 03 | lens |
| 06 | `docs` refresh `docs/context/` | P1 | 02, 03, 04 | lens |
| 07 | `feat` plugin hooks: recognize a proxy-fronted DeepSeek route | P0 | — | agentic-ai-artifacts |
| 08 | `docs` plugin README: document the proxy-fronted setup | P2 | 07 | agentic-ai-artifacts |

The dependency graph has two independent roots — `01` and `07` — with a maximum depth of three
beads and several chains tied at that length, including `01 → {02, 03} → 05` and `07 → 04 → 06`.
`02` and `03` are siblings (both depend only on `01`); both feed `05`, and both feed `06`
alongside `04`. The `03 → 05` step is the file-level guard, since both beads edit `README.md`.
`05` and `06` are independent — the two `docs` beads share no file and no semantic prerequisite,
so neither depends on the other; run them in whichever order is convenient. (An earlier draft's
`05 → 06` edge was a sequencing preference, not a dependency, and has been removed.)
Bead 07 is independent of every lens bead and is the actual bug fix — it should land first. It is
the piece that stops the 2x billing, but only once the overlay `~/.claude/.deepseek-env.json`
declares the proxy URL: with the overlay still naming `api.deepseek.com`, the predicate does not
recognize the route and the peak session proceeds (bead 08 supplies that setup; see §8.2 and §1).
It also carries the config-directory fold-in (§8.6): both hooks resolve `CLAUDE_CONFIG_DIR` before
falling back to the platform home helper. That is one line per file, in two files bead 07 already
edits, fixing the same class of defect as the predicate — an assumption about where `~/.claude`
is — so it rides along rather than becoming a bead that would touch the same files and the same
test harness.

Suggested order: `07 → 08 → 01 → 02 → 03 → 05 → 04 → 06`. Bead 04 is last among the code because
it verifies 07's behaviour from the lens side, and it is the one bead that can be dropped. It
carries two jobs at once, which is why it is specified case-by-case rather than sketched: it is
the only bead that *verifies* the no-plugin guarantee (§2) and the only bead that could *break*
it, since it is the only lens code that reads a file the plugin owns.

**Which beads a non-plugin user actually gets.** Beads 01, 02, 03, 05 and 06 are the whole of
that user's experience and touch the plugin not at all: correct peak pricing, a warning that says
why the number doubled, and docs that say so. Beads 04, 07 and 08 are the plugin integration, and
the absence of the plugin degrades lens by exactly one printed `doctor` row — which reads `pass`
with a note saying the plugin is not managing this machine.

## Change History

- **v7 (round-7 review)** — F7.1 (MINOR, correctness): §4.5:277-278, §8.6:463-464 and bead 04:83-84
  claimed the `provider_hooks` check "reads the same file the hooks do". The claim held for the JS
  hook only: the bash guard's overlay path `${HOME:-$USERPROFILE}` (`deepseek-peak-guard.sh:19`)
  preferred `HOME`, so on Windows — where Claude Code means `%USERPROFILE%\.claude` — it read a
  directory Claude Code ignores, and `doctor` could print `pass` for a route the guard would not
  recognize. Reviewed and *remedied by behaviour, not wording*: bead 07's spec corrects the guard's
  fallback to Claude Code's rule,
  `config_dir="${CLAUDE_CONFIG_DIR:-${USERPROFILE:-$HOME}/.claude}"`, so both hooks resolve the
  same directory on the unset path. The reviewer's wording remedy (rescope the sentences to the JS
  hook, add a divergence clause) was declined: it would keep the defect and document it, and rest
  on the §8.6 caveat F6.1 added rather than delete it. With the fix, §8.6's F6.1 divergence
  sentence is deleted, §2 item 5 returns to the "locate Claude Code's config directory the way
  Claude Code does" phrasing (now supportable), §8.6's "existing platform idiom" and "Not a
  regression" clauses are restated, and every "byte-identical when `CLAUDE_CONFIG_DIR` is unset"
  claim is rescoped to "identical wherever `HOME` and the platform home agree, deliberately
  changed where they diverge". Bead 07 gains an Outcome Definition bullet and a mirror-case test
  (unset `CLAUDE_CONFIG_DIR`, `USERPROFILE` and `HOME` differing) with a platform-precise
  guard/toggle assertion; bead 04 is verified unchanged. Specified only — the hook line is edited
  by whoever implements bead 07.

- **v6 (round-6 review)** — F6.1: §2's scope item 5 rescoped to claim only what the hooks actually
  do — honor `CLAUDE_CONFIG_DIR` when it is set, else fall back to their host language's existing
  home helper — dropping the unconditional "the way Claude Code does" parity claim, which the
  guard's `${HOME:-$USERPROFILE}` unset path does not satisfy on Windows. §8.6's residual-boundary
  paragraph gains one sentence naming that unset-path divergence (JS `os.homedir()` resolves
  `USERPROFILE` first, bash `${HOME:-$USERPROFILE}` prefers `HOME`) as deliberately preserved, not
  fixed. No behaviour, bead, or file-list change. F6.2: §7's relocated-config-directory bullet and
  bead 07's counterpart now pin the toggle's fold-in where a no-op cannot satisfy it — the absence
  of the `couldn't read` systemMessage off-peak, and a relocated peak case that must remove the
  overlay and the owned `settings` keys — so the "fails against either hook's pre-change code"
  claim holds for the toggle half too, and the claim states plainly which half each assertion
  holds for.
- **v5 (config-directory fold-in)** — the plugin's matching `CLAUDE_CONFIG_DIR` defect moves from
  *recorded* to *fixed*: §8.6 no longer leaves the toggle hardcoding `~/.claude` with an upgrade
  path, and bead 07 resolves the config directory in both hooks (`CLAUDE_CONFIG_DIR` first, then
  the existing platform idiom). §2 gains the scope item and drops the out-of-scope bullet that
  said the gap was not fixed; §3 records the evidence; §6 and §7 carry the file and test changes,
  including pinning the hook harness's config directory so it cannot read the developer's real
  one. Deliberately still out of scope: the plugin README's manual setup steps, which stay
  user-declared absolute paths.
- **v4 (standalone-usability pass)** — §2 states the no-plugin guarantee as a requirement rather
  than leaving it implicit, with the corresponding non-goal. §4.5 rewritten: the `provider_hooks`
  check is the only lens code that reads a plugin-owned file, and therefore the only place that
  guarantee can break, so it must be incapable of returning `FAIL` (a `FAIL` is a non-zero exit —
  `internal/cli/doctor.go:41-45`). Claude Code's config directory is resolved by a new
  `claudeConfigDir()` (`CLAUDE_CONFIG_DIR` → `%USERPROFILE%` → `$HOME` → `os.UserHomeDir()`) rather
  than the `$HOME`-first `config.userHomeDir()`, which was built to redirect *lens's own* files and
  is not Claude Code's precedence — on Windows the two disagree, and following the bead as written
  would have had the check read a file the hooks never touch. `internal/config/config.go` drops out
  of §6; `config.UserHomeDir` is not exported (bead 04). §4.3 and bead 03: `peak_pricing` promoted
  P1 → P0, because for a user running no guard it is the only peak signal that exists rather than a
  retrospective supplement. §7 gains the `internal/cli` verification section, isolating through
  `CLAUDE_CONFIG_DIR`. New §8.6 records that the plugin's toggle still hardcodes `~/.claude` and is
  silently inert under a relocated config directory — recorded, not fixed, with the one-line
  upgrade path. §10 states which beads a non-plugin user receives.
- **v3 (round-2 review)** — F2.1: bead 04's doctor check now evaluates the **hook predicate**
  (effective `ANTHROPIC_BASE_URL` from `settings.json` contains `deepseek`, else equals the
  overlay's declared URL, in that order), not overlay-vs-`cfg.ProxyAddr` equality, and its test
  specs cover all four clause combinations; §4.5 reworded to match. F2.2: §10 dependency shape
  restated as two independent roots (`01`, `07`) with several length-three chains tied, including
  `01 → {02, 03} → 05` and `07 → 04 → 06`; the `05 → 06` edge is dropped (bead 05's
  `Blocks` cleared, bead 03 gains `br-GI-4-05`). F2.3: §7 and bead 07 no longer attribute "lens is
  dropped" to the lens-URL-overlay config (its symptom is a needless rewrite); that outcome is
  reserved for the overlay-declares-direct config, which bead 07 does not fix. F2.4: bead 02/§7
  record that the pinned fixture gives equal `started_at` values, so
  `TestCostStepTakesEffectWithoutRestart`'s second call is incremented to keep `rows[0]` ordered by
  timestamp, not the index tiebreak. F2.5: bead 07/§4.1/§8.4 state the bash guard's `${VAR}`
  placeholder gap (recognized by the toggle, missed by the guard) as a known limit with an upgrade
  path, and the two predicates are described as the same *rule* per language, not identical code.
  *F2.1 correction (conductor, post-apply):* the warn is now gated on overlay **presence** — an
  absent `~/.claude/.deepseek-env.json` is always `pass` with a note, whatever the effective URL,
  and only a present overlay that declares a *different* address warns.
- **v2 (round-1 review)** — F1.1: §7/§8.3 now name the consumer fixture pin (`simpleCall()` at
  `consumer_test.go:38`, asserts at `:884`/`:961`/`:975`) that bead 02 must carry, so `go test
  ./...` holds at any wall-clock hour. F1.2: corrected the `Compute` test call-site count from 12
  to 11 (§6, §8.3, bead 02). F1.3: §1 and §10 no longer claim the fix unconditionally "stops the
  2x billing today"; both state it recognizes the route only when the overlay declares the proxy
  URL, matching §8.2. F1.4: §8.1 corrected — the three window copies are *unguarded against drift*;
  shared fixture dates make the suites analogous to read, not a cross-repo detector. F1.7: §4.3 the
  rule fires on `CostUSD != nil && *CostUSD > 0` (bead 03), so a zero-token $0 row raises no
  warning. F1.9: corrected the `NewRules` call-site count from four to eight (§4.2, §4.4, bead 03;
  bead 01 too). F1.10: §10 critical path restated as `01 → {02, 03} → 05 → 06`, with the `03 → 05`
  file-level guard named; no bead dependency changed. F1.12: §3 row now records issue #4 OPEN.
  Beads 01–04, 07 edited in the same round (see `planning/GI-4/review/round-1/changelog.md`).
