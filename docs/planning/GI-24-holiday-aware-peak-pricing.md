# GI-24 — Holiday-aware peak pricing, a configurable calendar, and per-session peak tagging

**Status**: converged (plan-conductor cross-review complete — 8 rounds, no open findings; ready for
beadification). v9 is a post-convergence **documentation-only** amendment made during beadification:
it changes no design decision, no file list, and no bead, and is recorded in the Change History.
**Version**: 9
**Issue**: [#24](https://github.com/abhisheksarkar30/deepseek-lens/issues/24) — created 2026-09-19,
after this plan converged and before bead 01 was implemented. No bead creates it; the nine beads
never mention the issue at all, so nothing downstream references the step this replaces. The number
was verified free against `develop` (`gh api .../issues/24` → 404, issues topped out at #21, PRs at
#23).
**Branch**: `GI-24-holiday-aware-peak-pricing`, cut from `develop`.
**Repo**: `deepseek-lens` only. Unlike GI-4 this story touches no second repository — but it *does*
leave the plugin's two hook copies of the peak window (see §8.1) one rule behind.

All line citations below were read on `develop` at `396dc76`'s merge (`4ed8cc8`), which is the branch
this plan is implemented on. GI-4's own citation discipline applies: a citation verified on someone's
working tree is worthless if the branch moved, so bead 01 re-checks the three counts marked ⚠ before
anything else.

---

## 1. Problem

**lens prices 19 weekdays of 2026 at double what DeepSeek charges for them.**

DeepSeek's published rule is that peak pricing applies during `01:00–04:00` and `06:00–10:00` UTC,
Monday to Friday, and that **everything else is off-peak — explicitly including weekends *and*
statutory public holidays** ([coverage](https://www.thestandard.com.hk/innovation/article/340702/DeepSeek-applies-off-peak-API-pricing-on-weekends-slashing-bills-by-half),
[rate detail](https://ofox.ai/blog/deepseek-api-price-increase-new-rates-peak-hours-cache-cost-2026/)).
Off-peak is exactly half of peak.

`pricing.IsPeak` ([internal/pricing/pricing.go:41-49](../../internal/pricing/pricing.go#L41-L49))
encodes the window and the weekend rule and **nothing else**. It has no notion of a holiday, so a
public holiday that falls on a weekday is classified as peak. In 2026 that is **19 weekdays**,
including National Day on Thursday 2026-10-01 and the whole Spring Festival working week
2026-02-16…20. Every call placed on those days is billed by lens at 2× when DeepSeek charged 1×.

This is the same *class* of defect GI-4 fixed, and it fails the same way. GI-4's §1 argued that a
silently halved total is the specific failure this repo's design exists to avoid; a silently doubled
one is its mirror image, and it is the direction that makes a user trust a number that is wrong.
The tool's thesis — a number is never invented (`CostSource` exists for exactly this reason) — is
violated by a hardcoded calendar that is simply incomplete.

**The calendar is not a fact lens can compute.** Chinese statutory holidays are administratively
declared each year, in a notice that also designates 调休 make-up work days. It cannot be derived;
it must be data. Today it is neither data nor correct — it is absent.

**Nothing surfaces the peak status of a session.** A per-call signal exists (`peak_pricing`,
[internal/analyze/kinds.go:27](../../internal/analyze/kinds.go#L27)), but the `sessions` table
carries only `warning_count` and the drill-down says nothing about how much of a session's spend was
peak-inflated. The user cannot answer "was this session expensive because of the work, or because of
the clock?" — which is the only actionable question peak pricing raises.

**And the current signal is about to become a lie.** `rulePeakPricing`'s detail text
([internal/analyze/rules.go:266](../../internal/analyze/rules.go#L266)) hardcodes
`"(01:00-04:00 or 06:00-10:00 UTC, Mon-Fri)"`. The moment a 调休 make-up day is honoured that
sentence is false on the day the rule fires on it — the six shipped 调休 days are all weekend dates
(§3), D3 prices their window like any weekday's, so `rulePeakPricing` fires there and the sentence
claims a Mon-Fri window the call was not billed under. A holiday in `OffPeakDates` is *not* the case:
D3 rule 1 makes such a day off-peak for the whole day, so the rule never fires on one, and the
sentence is never shown.

## 2. Scope

In scope:

1. A `pricing.Calendar` that decides peak vs off-peak for an instant, composing the existing window
   and weekend rules with a configured off-peak (rest-day) set and a work-day override set.
2. Two new configuration keys — `OffPeakDates` and `WorkDates` — following the `ModelMap` /
   `ModelMaxTokens` precedent in *shape* (a `Default*` const, a `LENS_*` env, a `config.toml` key, a
   flag; parsed by the consuming package), shipped with the **2026** calendar and its source cited.
   The precedent is followed in shape but not in failure mode: a bad `ModelMap` entry is skipped
   leniently, a bad date is fatal (see D2).
3. `peak_pricing`'s detail text corrected to describe the rule rather than a hardcoded window.
4. A per-session peak rollup — how many of a session's calls were peak-priced and what share of its
   spend they were — computed at read time, with **no schema change**.
5. `lens doctor` reporting the effective calendar and warning when it no longer covers the current
   year.
6. Docs on lens's side: README, the price-file header, and the Settings-tab wording that currently
   misdescribes who decides peak.

Explicitly out of scope:

- **No new SQL column, and no migration mechanism.** See §3 and D6 — this is a constraint the design
  is written *against*, not a deferral.
- **No retroactive repricing.** See D8.
- **The 2× multiplier stays a constant.** Decided, not deferred: `PeakMultiplier` keeps its existing
  `ponytail:` note naming "move it to config" as the upgrade path. The Settings tab keeps displaying
  it read-only.
- **No session-list column and no Stats-tab "peak spend" aggregate.** Both need a persisted per-row
  flag, which §3's constraint rules out; §8.5 records the upgrade path.
- **No edit path for the calendar in the dashboard.** `config.toml` has no write path anywhere in
  this repo (only `prices.toml` does, via `POST /api/prices`). The Settings tab displays the
  effective calendar read-only; editing is via file, env, or flag.
- **No change to the plugin's two hook copies of the window** (§8.1). They live in another repo and
  are already a recorded drift risk; this story widens the gap rather than closing it, and says so.

## 3. Verified current state

Every row was read out of source. Counts are stated because a stated count is this repo's recurring
review defect — ⚠ marks the three that bead 01 must re-verify before touching anything.

| Claim | Evidence |
|---|---|
| ⚠ `pricing.Compute` has exactly **1** production call site and **18** test call sites | `internal/consumer/consumer.go:430`; `internal/pricing/pricing_test.go` lines 24, 35, 46, 62, 78, 103, 125, 131, 139, 150, 173, 252, 253, 269, 273, 281, 289, 297 |
| ⚠ The package-level `pricing.IsPeak` has **1** production caller outside the package and **2** in-package call sites | `internal/analyze/rules.go:262`; `internal/pricing/pricing.go:149`, `:162` |
| ⚠ `analyze.NewRules` has **8** call sites (the list to its right *is* the eight — count them) | `internal/analyze/analyze.go:89`; `analyze_test.go:524`, `:542`, `:555`; `internal/cli/serve.go:85`; `internal/cli/replay_test.go:93`; `internal/consumer/consumer_test.go:91`; `internal/replay/replay_test.go:107` |
| `pricing.IsPeak` has **5** assertion sites across **4** test functions | `pricing_test.go:207`, `:220`, `:232`, `:235`, `:244` — in `TestIsPeakBoundariesWeekday` (`:190`), `TestIsPeakWeekendNeverPeaks` (`:213`), `TestIsPeakUsesUTCNotLocal` (`:228`), `TestIsPeakZeroTimeIsOffPeak` (`:240`) |
| `consumer.New` has **32** call sites (1 production) — **27** unqualified `New(` constructions in `internal/consumer/consumer_test.go` (it is `package consumer`, so an unqualified `New` *is* `consumer.New`) plus **5** outside that package | `internal/consumer/consumer_test.go` (27, incl. `:91`); `internal/cli/serve.go:85`; `internal/replay/replay_test.go:107`; `internal/cli/replay_test.go:93`; `internal/api/api_test.go:81`, `:582` |
| `SetPriceTable` has **5** call sites (1 production) | `internal/cli/serve.go:92`; `consumer_test.go:876`, `:922`, `:958`, `:1000` |
| `api.New` has **3** call sites | `internal/cli/serve.go:114`; `internal/replay/replay_test.go:122`; `internal/cli/replay_test.go:105` |
| `api` takes optional capabilities through setters, and "leaving it unset is a supported state" | `internal/api/api.go:102-115` (`SetPricing`, `SetRetention`), and the same pattern on the consumer (`SetPriceTable`, `SetSessionAggregator`, `SetBodyDecoding`, `internal/consumer/consumer.go:127-151`) |
| `consumer.Consumer` reaches the money path through a `PriceTable` field | `internal/consumer/consumer.go:102`, `:131` |
| `ruleInput` already carries the whole row and the resolved `Rules` | `internal/analyze/rules.go:16-21` |
| `Rules` is a struct with config-resolved tables baked in | `internal/analyze/analyze.go:52-56` |
| **There is no migration mechanism.** `schema.sql` is applied with `CREATE TABLE IF NOT EXISTS` only, and doctor reports the absence out loud | `internal/store/schema.sql:1-5`; `internal/store/store.go:91-93`, `:121`; `internal/cli/doctor.go:133` — `"n/a — no migrations table in v1"` |
| `sessions` carries `warning_count` and token/cost totals, and no peak notion | `internal/store/schema.sql:55-68` |
| `getSession` already loads the session's calls before rendering — up to `store.DefaultLimit` (1000) per session | `internal/api/api.go:944` — `ListRequests(ctx, store.Filter{SessionID: id})`, no `Limit`, so the store applies its default cap |
| `config`'s table-valued settings are plain strings with a `Default*` const, a `LENS_*` env, a `config.toml` key, and a flag — parsed by the consuming package, not by `config` | `internal/config/config.go:26`, `:36`, `:81-82`, `:128-129`, `:204-209`, `:237-238`; parsed at `internal/analyze/analyze.go:68-75` |
| `config.Validate` rejects nonsensical values with the offending field and value named, and a rejection prevents startup | `internal/config/config.go:285-289` (`BodyPolicy`) |
| The settings UI currently claims the *upstream* signals the peak window | `internal/web/index.html:151-153` |
| `readme_test.go` enforces **equality** between `AllKinds()` and the README's warning rows, so a kind's sentence cannot change without the README changing | `internal/analyze/readme_test.go` (cited by GI-4 §3) |
| The 2026 statutory-holiday notice is 国办发明电〔2025〕7号, 2025-11-04 | [gov.cn](https://www.gov.cn/zhengce/zhengceku/202511/content_7047091.htm) |

**The 2026 calendar, with every weekday independently re-computed (`date -d`), not taken from the notice's prose:**

| Holiday | Dates | Weekdays in range |
|---|---|---|
| 元旦 | 2026-01-01 … 01-03 | Thu 01, Fri 02 |
| 春节 | 2026-02-15 … 02-23 | Mon 16, Tue 17, Wed 18, Thu 19, Fri 20, Mon 23 |
| 清明 | 2026-04-04 … 04-06 | Mon 06 |
| 劳动节 | 2026-05-01 … 05-05 | Fri 01, Mon 04, Tue 05 |
| 端午 | 2026-06-19 … 06-21 | Fri 19 |
| 中秋 | 2026-09-25 … 09-27 | Fri 25 |
| 国庆 | 2026-10-01 … 10-07 | Thu 01, Fri 02, Mon 05, Tue 06, Wed 07 |

**19 weekday holidays.** The 调休 make-up work days, all of which fall on a weekend and therefore
need `WorkDates` to be expressible at all: 2026-01-04 (Sun), 2026-02-14 (Sat),
2026-02-28 (Sat), 2026-05-09 (Sat), 2026-09-20 (Sun), 2026-10-10 (Sat) — **6 days**.

## 4. Design

### D1. The calendar lives in `pricing`, because `pricing` owns the window

`internal/pricing` is already the leaf package that owns money and the peak window, and it already
imports only `parse` (GI-4 §4.4). The calendar is the window's other half, so it goes beside it in a
new `internal/pricing/calendar.go`:

```go
// Calendar decides peak vs off-peak for an instant: DeepSeek's peak window and
// weekend rule, plus two configured date sets that answer the rest-day
// question — an off-peak (rest-day) set and a work-day set.
//
// The zero value is meaningful and is exactly the pre-holiday behaviour: no
// dates configured, so IsPeak is the window-and-weekend rule alone. That is
// what keeps every existing test and every un-wired caller honest without a
// change, and it is why the calendar is installed optionally (D7).
type Calendar struct {
	offPeak map[string]bool // "2006-01-02" UTC → a rest day: off-peak for the whole day
	work    map[string]bool // "2006-01-02" UTC → a work day: peak inside the window, like any weekday (調休)

	// the two strings NewCalendar was given, kept so DateSets can echo them
	// back verbatim — the read-only Settings display (D7) and doctor's print
	// of the effective calendar (D9) both read this
	offPeakSrc string
	workSrc    string
}

// NewCalendar parses the two date sets. Both may be empty.
func NewCalendar(offPeak, work string) (Calendar, error)

// DateSets returns the two configured date strings exactly as supplied to
// NewCalendar — the verbatim echo, not a re-render of the parsed maps.
// Settings and doctor show these read-only, and both must show what the
// config actually says: a range comes back as its range
// ("2026-02-15..2026-02-23"), not expanded into nine dates, and the string
// matches config.toml / env / flag byte for byte so the user can compare it.
// The zero calendar returns ("", "").
func (c Calendar) DateSets() (offPeak, work string)

// IsPeak reports whether t is billed at DeepSeek's peak rate.
func (c Calendar) IsPeak(t time.Time) bool

// Covers reports whether year is named by either configured date set. It exists
// for doctor's coverage check (D9), not for pricing — IsPeak never consults it.
func (c Calendar) Covers(year int) bool

// PeakPriced is the one predicate behind both the peak_pricing warning and the
// per-session rollup (D5).
func (c Calendar) PeakPriced(at time.Time, cost *float64) bool
```

### D2. Date-list grammar, and why a bad date is fatal where a bad `ModelMap` entry is not

The grammar is deliberately the smallest thing that mirrors the source document:

```
# OffPeakDates — the notice's holiday ranges
2026-01-01..2026-01-03,2026-02-15..2026-02-23,...
# WorkDates — the notice's 调休 make-up work days
2026-01-04,2026-02-14,...
```

Comma-separated items, each `YYYY-MM-DD` or `YYYY-MM-DD..YYYY-MM-DD` inclusive, whitespace trimmed,
empty string meaning the empty set. Ranges are not a convenience — the notice *is* seven ranges, and
the shipped default should read like the notice it cites.

**A malformed item is an error, not a skipped entry.** This is a deliberate departure from
`ModelMap`/`ModelMaxTokens`, which skip bad entries leniently
([internal/analyze/analyze.go:64-67](../../internal/analyze/analyze.go#L64-L67)). The difference is
the failure direction: a skipped model mapping degrades to a fallback that is *visible* in the
dashboard's model column, whereas a silently skipped holiday date is a silent 2× overcharge on
exactly the days this story exists to fix. Lenient parsing here would reintroduce the bug in a
harder-to-see form.

The grammar is therefore owned once, by `pricing.NewCalendar`, and `config.Validate` calls it purely
to reject a typo at startup — the same treatment `BodyPolicy` gets
([internal/config/config.go:285-289](../../internal/config/config.go#L285-L289)). The check lives in
`Validate`, which `config.Load` does **not** call
([internal/config/config.go:245-274](../../internal/config/config.go#L245-L274)): `serve` and
`doctor` call it explicitly ([serve.go:39](../../internal/cli/serve.go#L39),
[doctor.go:95](../../internal/cli/doctor.go#L95)), so `serve` refuses to start on a bad date and
`doctor` reports it as a `config_valid` FAIL and exits non-zero; commands that never call `Validate`
(`ls`, `purge`, `replay`, …) are unaffected. The rejection fires at the point of use, not at load.

*This introduces a `config → pricing` import edge.* It is safe (no cycle; `pricing` imports only
`parse`) and it buys a single grammar. The alternative — a second date parser in `config` accepting
pre-parsed data in `pricing` — splits one grammar across two packages and is worse. Recorded here so
the reviewer can challenge it rather than discover it.

### D3. Precedence, and why it is stated as a rule rather than left to map lookups

The whole of `IsPeak` is one question — *is this a rest day?* — followed by one window rule. The two
configured sets are **both rest-day overrides, and therefore symmetric**: `OffPeakDates` forces a
date to be a rest day, `WorkDates` forces it to be a work day, and a date is in neither or exactly
one of them. `IsPeak` resolves in this order:

1. the date is in **`OffPeakDates`** → **off-peak for the whole day**;
2. else the date is in **`WorkDates`** → apply the window rule, **skipping the weekend check** (a
   make-up work day is a working day);
3. else Saturday or Sunday → **off-peak**;
4. else inside `01:00–04:00` or `06:00–10:00` UTC → **peak**;
5. else → **off-peak**.

Rules 2 and 3 make the symmetry explicit: a work day — reached by default (a weekday) or by
`WorkDates` (a 调休 weekend, which *must* beat the weekend rule or it is inexpressible) — peaks only
*inside the window*; a rest day — reached by default (a weekend) or by `OffPeakDates` (a weekday
holiday) — never peaks. That symmetry is the whole reason the model is correct, and it is exactly
what v1's D3 got wrong: v1 classified a date in the 调休 set as peak *for the whole day*, which
overcharges every off-window hour of the 6 make-up days — the silent 2× overstatement this story
exists to remove. **The rejected alternative — a literal whole-day peak on the 调休 set — is wrong
under either reading of the 调休 ambiguity**: if DeepSeek treats a make-up Saturday as a working day
it bills the window like any weekday, and if it treats it as a rest day the day is off-peak; no
reading yields peak around the clock.

A date present in **both** sets is a contradiction and `NewCalendar` rejects it, naming the date.
That is the one case where "last rule wins" would silently produce an arbitrary answer.

### D4. The date key is the UTC date, and that is load-bearing

DeepSeek's window is published in UTC; the holidays are published in Beijing dates. Keying the sets
by the UTC date is correct, and non-obviously so, so the reasoning is written into the code as a
comment rather than left implicit:

> Beijing date *D* spans `[D-1 16:00 UTC, D 16:00 UTC)`. The peak windows are `01:00–04:00` and
> `06:00–10:00` UTC, both inside the `00:00–16:00 UTC` half of that span, so every peak window on
> Beijing date *D* carries UTC date *D*. **The two keyings therefore agree** — and they agree only
> *because* D3 makes both sets window-scoped: neither a 调休 work day nor an off-peak holiday changes
> an off-window instant's answer on its own (both sets are the same answer the weekday/weekend rule
> already gives outside the window), so the only instants where either set can matter are peak-window
> instants, and a peak window never straddles the `16:00 UTC` date boundary.

The condition this rests on — that both windows sit in the UTC `00:00–16:00` half of the day, which
is what makes a window instant carry the same UTC and Beijing date — is recorded as a `ponytail:`
note. It is load-bearing, and v1 rested on it too: that argument survives v1's F1.1 defect (its
premise never mentioned the whole-day override, and a peak window is entirely within the UTC
`00:00–16:00` half regardless), which is why F1.1's bug is a *pricing* defect and not a keying one.
What v1 got wrong was the *test*: §7's keying case was written against an off-peak holiday, where
the two keyings agree trivially, so it could not have caught the whole-day-peak bug. §7 now carries
the case that does.

The counterexample that names the corrected model's answer: **2026-02-14, a 调休 Saturday, at
`16:30 UTC`.** UTC keying reads `2026-02-14` → in `WorkDates` → window rule, off-window → **off-peak**.
Beijing keying reads `2026-02-15 00:30` → in the Spring Festival `OffPeakDates` → **off-peak**. Both
keyings agree, and both answer off-peak — the exact case v1's whole-day peak would have priced at 2×.

If DeepSeek ever moves a window across `16:00 UTC`, UTC keying and Beijing keying part ways and this
comment is the thing that says so. Keying by a `UTC+8` constant would work equally today and is
rejected only because it puts a timezone constant into a package that is otherwise purely UTC.

### D5. One predicate, because the warning and the rollup must not be able to disagree

`PeakPriced(at, cost)` — `cost != nil && *cost > 0 && c.IsPeak(at)` — is the exact gate
`rulePeakPricing` uses today ([internal/analyze/rules.go:259-264](../../internal/analyze/rules.go#L259-L264)),
lifted into `pricing` so the per-session rollup in `internal/api` calls the *same function* rather
than restating it. A second copy would drift, and its drift would show as a session whose tagged
count disagrees with its own calls' badges. `internal/api` already imports `pricing`
([internal/api/prices.go:10](../../internal/api/prices.go#L10)), so no new edge appears.

**What the shared predicate guarantees, scoped honestly.** It guarantees the warning and the rollup
agree when both are evaluated under the *same* calendar. It does **not** guarantee they agree across
a calendar change, and it never could: the warning is written at ingest and the rollup is computed at
read time (D6, D8), so a row ingested under one calendar and served under another is precisely the
case where the two legitimately differ. §D6 and §D8 state that split as the documented, intended
answer rather than leaving §D5 to assert a guarantee the design cannot keep.

The gate keeps its meaning: a `nil` cost is an unpriced call and a configured model with zero tokens
prices to a real, non-nil `0`. Neither can honestly be called "billed at peak", and §4.4 of GI-4
made that argument first.

### D6. The session rollup is computed at read time, because the schema cannot change

`getSession` already loads the session's calls
([internal/api/api.go:944](../../internal/api/api.go#L944)) to render the drill-down, so the rollup
is a loop over rows already in memory (up to the `DefaultLimit` bound, addressed below). `sessionDetail` gains one field:

```go
// sessionPeak is how much of a session's spend landed on DeepSeek's peak rate.
// Calls counts the calls PeakPriced accepted, not the calls merely inside
// the peak window: an unpriced call placed at peak is not "priced under peak
// hours" and must not be counted as though it were.
type sessionPeak struct {
	Calls   int     `json:"calls"`     // priced calls placed at peak; exact over the loaded rows
	CostUSD float64 `json:"cost_usd"`  // those calls' cost; the share is CostUSD / Session.TotalCostUSD — the denominator is the full-session aggregate, so the two sets differ (see the cap note below)
}
```

`Session.TotalCostUSD` is the denominator, so the UI can state a share without re-summing. **A
session can have `TotalCostUSD = 0` with calls** — an all-unpriced session, the shape
`store_test.go:829` already asserts — and then `Calls` is 0 too, so a UI that divides
`CostUSD / TotalCostUSD` renders `0/0`. The intended rendering for that zero denominator is to
**show the peak count and omit the share** (absent, not `NaN%`); §7 pins it.

**No column, and that is a hard constraint rather than a preference.** `schema.sql` is applied with
`CREATE TABLE IF NOT EXISTS` ([internal/store/schema.sql:1-5](../../internal/store/schema.sql#L1-L5)),
so an existing `lens.db` never gains a column, and `doctor` reports the absence of a migrations
mechanism out loud ([internal/cli/doctor.go:133](../../internal/cli/doctor.go#L133)). Adding a
per-row flag would require inventing that mechanism — a strictly larger change than this story, and
one that touches a decision the repo made deliberately. §8.5 records what that would unlock.

The cap is `store.DefaultLimit` = 1000 calls per session
([internal/store/store.go:39](../../internal/store/store.go#L39)), which bounds the loop — and the
rollup **inherits it**: it runs over the newest 1000 calls of *this* session, so a session with more
than 1000 calls has its peak count computed over a truncated set and undercounts. That truncation is
the one the drill-down already carries (it renders those same 1000 rows), so `Calls` and the list stay
consistent — but the plan states the shared cap rather than claiming the chosen design has none,
because §D6 turns down the alternative below partly *for* a cap like this one.

**The share's numerator and denominator are not drawn from the same set, and that is stated rather
than implied.** `CostUSD` sums the newest ≤1000 loaded rows (`getSession`'s `ListRequests`,
[internal/api/api.go:944](../../internal/api/api.go#L944), no `Limit`, so bounded by
`store.DefaultLimit`), while the denominator `Session.TotalCostUSD` is the session's running aggregate
over **all** its calls — the `sessions.total_cost_usd` column, scanned at
[internal/store/store.go:641](../../internal/store/store.go#L641), updated at `:1160`, fed
incrementally by [internal/session/session.go:135-149](../../internal/session/session.go#L135-L149).
For a session with more than 1000 calls the two diverge, so `CostUSD / Session.TotalCostUSD`
**understates** the peak share while `Calls` stays exact. So "header and list stay consistent" holds
for `Calls`, not for the share; the plan states the asymmetry rather than capping the denominator,
because capping it would make the share peak spend over a truncated session spend — a different,
arbitrary quantity, not what `Session.TotalCostUSD` means.

**The rollup runs on the calendar in force *now*, and it can therefore disagree with a stored badge
— deliberately.** Cost and `warnings` are frozen at ingest (D8); the rollup is recomputed against
*today's* calendar. So a row ingested before this fix on a 2026 holiday keeps both its stored doubled
`cost_usd` and its stored `peak_pricing` warning, while this rollup — run on the current calendar —
counts it as not-peak. The session header and that row's badge will therefore disagree, and that is
the intended answer: the badge reports what the call actually cost when it was ingested, the rollup
reports what the session's spend looks like under the calendar now in force. Any later calendar edit
(a user's, or the 2027 default) produces the same split for every pre-existing row. §D5's guarantee
is scoped to match, and §7 pins this behaviour with a test rather than hiding it.

*The rejected alternative* — derive the rollup from the session's stored `peak_pricing` warnings
instead of recomputing — is worse, and concretely so: `getSession` fetches warnings through
`ListWarnings{Filter{Limit: store.DefaultLimit}}` and filters down to the session's ids *afterwards*
([internal/api/api.go:958-968](../../internal/api/api.go#L958-L968)), so a session whose warnings
fall outside the newest `DefaultLimit` rows would silently undercount. Its cap is *global*, not
per-session: a small session can lose every one of its warnings because some other session generated
a thousand. The rollup would report a peak count that is quietly too low, and — unlike the chosen
design's per-session cap, which truncates exactly the rows the drill-down already shows — with no
visible cue.

### D7. The calendar reaches the consumer and the API through the existing optional-setter seam

Three install points, each mirroring an existing one so neither `api.New` nor `consumer.New` changes
signature — the sole signature change is `analyze.NewRules`' third argument, below:

| Install | Mirrors | Consequence of leaving it unset |
|---|---|---|
| `Consumer.SetCalendar(pricing.Calendar)` | `SetPriceTable` (`consumer.go:131`) | calls price with the window-and-weekend rule only — the pre-GI-24 behaviour |
| `api.SetCalendar(pricing.Calendar)` | `SetPricing` / `SetRetention` (`api.go:106`, `:112`) | behaves as today: the rollup applies the window-and-weekend rule with no holidays, so it reports the same numbers it would pre-GI-24 — the zero calendar *is* that rule (D1). The *same* installed value supplies `/api/prices`'s `off_peak_dates` / `work_dates` through `DateSets()` (D1), so those fields echo `""`/`""` when unset — no separate install |
| a `calendar` field on `analyze.Rules`, via `NewRules`'s **new third parameter** | the existing `modelMap`/`maxTokens` fields | behaves as today: `rulePeakPricing` fires on every weekday call inside the window, exactly as now — the zero calendar is the window-and-weekend rule, not an inert one |

`api.New`'s 7-parameter signature and its **3** call sites are therefore untouched, and
`consumer.New`'s **32** are untouched (**27** unqualified `New(` constructions inside
`internal/consumer/consumer_test.go`, which is `package consumer`, plus **5** in other packages —
`serve.go:85`, `api_test.go:81`/`:582`, `cli/replay_test.go:93`, `replay/replay_test.go:107`).
`analyze.NewRules` is the one constructor that must change, because `Rules` is a value with its
config-resolved tables baked in and the calendar is a third such table — **8 call sites: 4 inside
`internal/analyze`** (`analyze.go:89`'s `defaultRules`, which passes the zero calendar and so keeps
its current behaviour, plus the three in `analyze_test.go`) **and 4 outside** (`serve.go:85`,
`internal/cli/replay_test.go:93`, `internal/replay/replay_test.go:107`,
`internal/consumer/consumer_test.go:91`).

**The live risk this creates is that `serve.go` forgets a setter, and the whole story silently does
nothing** — invisible precisely *because* the zero calendar is silently identical to today's rule —
the same class of silence GI-4 §4.5 built a doctor check to expose. The third install point is
compile-enforced (the `NewRules` argument is required once bead 04 changes the signature), so the
live risk is the two *setters* alone. Bead 06 pulls those two calls out of `Serve` into a named
helper `wireCalendar(cal pricing.Calendar, cons *consumer.Consumer, dash interface{ SetCalendar(pricing.Calendar) })`
— the same "split out from `Serve` so the wiring is testable" pattern `checkRedaction` and
`purgeOnStartup` already use ([serve.go:186-217](../../internal/cli/serve.go#L186-L217)) — and a new
`internal/cli/serve_test.go` calls it and asserts the calendar is in force on both seams: the API
echoes the configured `off_peak_dates` / `work_dates` from `/api/prices` (the `SetCalendar` →
`DateSets()` path, D1), and the consumer prices a call at a configured holiday instant at 1×. Bead
07's doctor check then prints the effective calendar so a user can see what is actually in force.

### D8. Forward-only, stated rather than implied

`cost_usd` and the `warnings` table are written at ingest and nothing in this repo recomputes them.
Fixing the calendar therefore **cannot repair rows already mispriced at 2× on a 2026 holiday**; those
rows keep both their doubled cost and their `peak_pricing` warning, because that is what the pricing
rule of the day actually did. The README and the plan both say this plainly. A user who wants those
numbers corrected has no path in this story, and inventing one (a reprice command, a reprice-on-read
rule) is a larger design question than this story.

**The one visible consequence, stated so it is not a surprise.** A pre-fix holiday row keeps its
doubled `cost_usd` and its `peak_pricing` warning, but the per-session rollup (D6), computed at read
time on the current calendar, will not count it. So a session header can read `Calls = 0` while the
drill-down lists a `peak_pricing` warning on one of its rows — the badge reporting what the call cost
then, the rollup reporting the session under the calendar now in force. That split is the documented,
intended behaviour, not a bug: §D6 records why the alternative (deriving the rollup from the stored
warnings) is worse, and §7 pins it with a test.

### D9. `lens doctor` makes the staleness visible

The shipped default is 2026 dates. In 2027 the calendar still parses, still applies, and is now
wrong — silently, in the **overstating** direction, which is the direction that erodes trust in every
other number lens prints. The default is a dated fact, exactly like `ModelMaxTokens`'s
`ponytail:`-marked placeholder ceilings, so the mitigation is not to make it self-updating (it
cannot be) but to make its expiry *visible*.

A new check reports the effective calendar's coverage — `Calendar.Covers(year)` (D1), which asks
whether today's year is named by either configured set — and warns — never fails — when today's year
is not covered. This follows GI-4 §4.5's rule for `provider_hooks` exactly: *this* check must be
incapable of returning `FAIL`, because `runDoctor` turns a `FAIL` into a non-zero exit
([internal/cli/doctor.go:41-45](../../internal/cli/doctor.go#L41-L45)) and lens's correctness must
not depend on a date the user has not yet updated. The "never FAIL" rule is scoped to the coverage
check, which is a different check from `config_valid`. A malformed date string *does* reach it:
`runChecks` short-circuits on nothing — it appends the `config_valid` FAIL and runs every later check
in the same pass ([internal/cli/doctor.go:95-99](../../internal/cli/doctor.go#L95-L99)) — so the
coverage check must guard its own construction. It does: if `pricing.NewCalendar` returns an error,
the check **skips and appends nothing** (the malformed string is already reported as `config_valid`),
leaving it incapable of `FAIL` on any input, well-formed or not.

## 5. Data flow

Unchanged in shape. The calendar is a new *input* to two existing steps and reorders nothing.

```
client -> proxy -> (tee) -> sink -> consumer
                                     |
                                     +- ExtractMeta / ExtractUsage
                                     +- resolve session
                                     +- cost step:  pricing.Compute(model, usage, table, started_at, cal)
                                     |                 ^ still before InsertRequest; cal is a field, read once
                                     +- InsertRequest
                                     +- analyzers:  ... rulePeakPricing -> cal.PeakPriced(started_at, cost_usd)

dashboard: GET /api/sessions/{id} -> ListRequests(session) -> sessionPeak rollup (cal.PeakPriced per row) -> JSON
```

The hot path is untouched: the calendar is consulted only on the cold path, and only as two map
lookups per call. No new per-request work on the proxy listener.

## 6. Changes

| File | Change |
|---|---|
| `internal/pricing/calendar.go` | **new** — `Calendar`, `NewCalendar`, the date grammar, `IsPeak`, `PeakPriced`, `DateSets()` (echoes the two configured strings verbatim — the seam bead 05's Settings display and bead 07's doctor print read), `Covers(year)` for doctor's coverage check (D1/D9; bead 01 owns both accessors), the UTC-keying note (D4) |
| `internal/pricing/calendar_test.go` | **new** — grammar, ranges, the both-sets rejection, precedence (D3), UTC keying (D4), `Covers` |
| `internal/pricing/pricing.go` | `Compute` takes a `Calendar`; the two `IsPeak(at)` calls hoist to one `peak := cal.IsPeak(at)`; the package-level `IsPeak` is **removed** so there is exactly one way to ask |
| `internal/pricing/pricing_test.go` | **18** `Compute` call sites gain a calendar; the **5** assertion sites across **4** functions of the old `IsPeak` tests become method calls on a zero calendar; new peak/off-peak holiday cases |
| `internal/config/config.go` | `DefaultOffPeakDates`, `DefaultWorkDates`, two `Config` fields, two env names, two `applyKV` cases, two flags, one `Validate` check calling `pricing.NewCalendar(offPeak, work)` — a *single* call passing both strings, which is what lets it reject a date in both sets (D2/D3) |
| `internal/config/config_test.go` | both keys through file, env, flag; malformed date rejected naming the item; a date in both sets rejected |
| `internal/analyze/analyze.go` | `Rules` gains a `calendar` field; `NewRules` gains a third parameter; `defaultRules` passes the zero calendar |
| `internal/analyze/rules.go` | `rulePeakPricing` calls `in.opts.calendar.PeakPriced(...)`; the hardcoded detail sentence at `:266` (`"(01:00-04:00 or 06:00-10:00 UTC, Mon-Fri)"`) is rewritten to describe the rule — the §1/§2.3 fix, without which it names a Mon-Fri window on a 调休 weekend it fires on |
| `internal/analyze/kinds.go` | `KindPeakPricing`'s sentence describes the rule, not a hardcoded window |
| `internal/analyze/analyze_test.go` | **3** `NewRules` call sites; holiday cases — a holiday weekday is not peak, a 调休 Saturday is, an unpriced holiday call still raises nothing |
| `README.md` | the `peak_pricing` row inside the `BEGIN/END warning kinds` markers (enforced by `readme_test.go`); the peak-pricing prose; the forward-only note (D8) |
| `internal/consumer/consumer.go` | `SetCalendar`; the `Compute` call at `:430` passes the field |
| `internal/consumer/consumer_test.go` | a holiday-priced row asserts 1× where the same row on a plain weekday asserts 2× (bead 02); its `NewRules` call site (`:91`) gains the calendar argument in bead 06, with the other external call sites — v2 owned it in bead 02 only, which runs before the signature exists |
| `internal/api/api.go` | `SetCalendar`; `sessionPeak` type; `sessionDetail` gains the field; `getSession` computes it |
| `internal/api/api_test.go` | drill-down rollup cases: mixed peak/off-peak, all-peak, unpriced-at-peak (counts zero), empty session, and an all-unpriced session (`TotalCostUSD = 0`) whose share is a defined zero rather than `NaN` (D6) |
| `internal/api/prices.go` | `pricesResponse` carries the effective `off_peak_dates` / `work_dates` so Settings can show them (read-only); both wire names match their config keys (`WorkDates`, not "peak date"). The values are the installed `Calendar`'s `DateSets()` (bead 01) read through `a.calendar` (the `SetCalendar` seam), threaded into `renderPrices` (`:136`) so both GET (`:51`) and POST (`:119`) render them; the zero calendar echoes `""`/`""` |
| `internal/api/prices_test.go` | the two new response fields, asserting they echo the configured strings verbatim (`DateSets`, D1) |
| `internal/pricing/table.go` | the price-file header comment (`:236-238`) — it names the peak window and says lens applies the multiplier itself; after this the window is calendar-driven, so the sentence is qualified to say holidays can override it |
| `internal/cli/serve.go` | construct the calendar from config once; install it on the consumer and the API, and pass it as `NewRules`' third argument at `:85` (D7's third install point — the same line builds the consumer) |
| `internal/cli/serve_test.go` | **new** — bead 06's wiring assertion: install a config-built calendar through the extracted `wireCalendar(cal, cons, dashAPI)` helper and assert it lands on both cold-path seams — the API echoes the configured `off_peak_dates` / `work_dates` from `/api/prices` (the `SetCalendar` → `DateSets()` path, D1), the consumer prices a call at a configured holiday instant at 1× |
| `internal/cli/replay_test.go` | its `NewRules` call site (`:93`) gains the calendar argument — omitted from v1, which left the tree uncompilable after the signature change |
| `internal/replay/replay_test.go` | its `NewRules` call site (`:107`) gains the calendar argument — the second site v1's §6 omitted |
| `internal/cli/doctor.go` | the coverage check (D9) via `Calendar.Covers`, guarding its own `NewCalendar` construction so a malformed date skips it instead of adding a second FAIL; the effective calendar in the printed config |
| `internal/cli/cli_test.go` | the check's cases — covered year passes, uncovered warns, never fails |
| `internal/web/app.js` | render the session rollup in the drill-down; render the effective calendar (`off_peak_dates` / `work_dates`) read-only in Settings; a zero session spend shows the peak count with no share, never `NaN%` (D6) |
| `internal/web/index.html` | the drill-down markup the rollup renders into (**bead 05**); the effective calendar displayed read-only (**bead 05** — it renders the `/api/prices` fields bead 05 adds); Settings wording corrected (`:151-153`) (**bead 08** — docs prose) |
| `docs/context/*.md` | Phase 5.6 refresh — `data-model.md` (no schema change, but the session rollup is a new response shape), `api-surface.md` (the drill-down shape, the `/api/prices` fields), `build-and-run.md` (two new env vars), `workflows.md` (the cost step's new input), `testing-and-quality.md` (the paragraph at `:66-71` names the removed `pricing.IsPeak` and describes the weekday/weekend + UTC-boundary peak tests bead 02 rewrites), `cli-and-tooling.md` (the `doctor` row at `:10` enumerates the PASS/WARN/FAIL checks and printed config, both of which this plan changes — §D9 adds the coverage check, §6's `doctor.go` row adds the effective calendar) |

## 7. Test strategy

**Unit — `internal/pricing/calendar.go`** (the money path; highest value):
- Grammar: a single date; a range; several comma-separated items; surrounding whitespace; the empty
  string as the empty set; each malformed shape (missing zero-padding, a reversed range, a trailing
  comma, a non-date) rejected with the offending item named.
- The same date in both sets is rejected, naming the date (D3).
- **Precedence, each rule pinned independently**: a `WorkDates` date at `20:00 UTC` (off-window) is
  off-peak while *the same date* at `02:00 UTC` is peak — the case v1 could not express, and the one
  that catches a whole-day-peak regression (F1.1); a weekday holiday in `OffPeakDates` at `02:00 UTC`
  is off-peak (it must beat rule 4); a plain Saturday at `02:00 UTC` is off-peak; a plain Wednesday
  at `02:00 UTC` is peak.
- **UTC keying (D4)**: a holiday expressed in Beijing time on both sides of the `16:00 UTC`
  boundary classifies the same as the UTC-keyed date — i.e. `IsPeak` at `D-1 16:30 UTC`,
  `D 00:30 UTC`, `D 02:00 UTC`, `D 09:00 UTC` all return the holiday's answer — **and** the
  counterexample the corrected model must answer off-peak: `2026-02-14` (a 调休 Saturday) at
  `16:30 UTC` is off-peak, matching the Beijing-keyed `2026-02-15 00:30` holiday reading. v1's
  keying test used an off-peak holiday and could not distinguish the two keyings; this one can.
- **The zero calendar behaves exactly as the old package function did** — the four existing
  `IsPeak` test functions, unchanged in expectation, run against `Calendar{}`. This is the regression
  guard that lets 18 `Compute` sites be edited mechanically.
- **`Covers(year)`**: true for a year named by either set, false for a year named by neither.

**Unit — `internal/pricing`**: `Compute` at a holiday is exactly 1× the off-peak amount where the
same usage on the same weekday a week later is exactly `PeakMultiplier`× — integer equality on
micro-dollars, not a tolerance (the existing peak test's style, `pricing_test.go:252-253`).

**Unit — `internal/config`**: both keys through file, env and flag; a malformed date fails
`Validate` with the item named (the `config_test.go:216-222` style, targeting `Validate` — `Load`
does not call it, D2); a date in both sets fails; the shipped defaults parse and cover 2026.

**Unit — `internal/analyze`**: `peak_pricing` fires on a 调休 Saturday, does not fire on a National
Day weekday, does not fire on an unpriced call on either, and `readme_test.go` covers the README row
by construction once the sentence changes. **The fired detail text no longer names a Mon-Fri window**
on the 调休 Saturday where the rule fires but the day is not a weekday — the §1/§2.3 sentence,
asserted rather than left to the implementer.

**Unit — `internal/api`**: the rollup cases above, plus that `Calls` counts only calls
`PeakPriced` accepted — the case that would pass if the rollup restated `IsPeak` instead of calling
`PeakPriced`. **And the documented cross-calendar split (D6/D8)**: a session holding a stored
`peak_pricing` warning on a row whose calendar now says not-peak yields `Calls = 0`, on purpose — the
badge and the rollup are allowed to disagree, and this test pins that they do rather than hiding it.
All four v1 cases used one calendar and could not have caught this.

**Unit — `internal/cli`**: `doctor` passes on a covered year and warns on an uncovered one, and
**the coverage check never changes the exit code** — with well-formed dates the exit stays zero
whether the year is covered or not; with a malformed date the exit is non-zero _only_ because
`config_valid` FAILs, and the coverage check skips rather than adding a second FAIL. That
invariant — the coverage check is incapable of `FAIL` on any input — is D9's whole point, and the one
place this story could make lens depend on user-entered data. **And the `serve` wiring assertion
(D7)**: the new `serve_test.go` installs a config-built calendar through `wireCalendar` and asserts
it lands on both cold-path seams — the API echoes the configured `off_peak_dates` / `work_dates`
from `/api/prices`, and the consumer prices a configured holiday instant at 1×.

**Integration / E2E**: deferred to `/develop-tests`. The natural one is the claim the story makes end
to end — a call through lens on a pinned 2026 holiday records `cost_usd` at 1× with no
`peak_pricing` warning, while the same call on the adjacent ordinary weekday records 2× with one.

**What is deliberately not tested**: the plugin's hook copies of the window (§8.1). They are in
another repository with no shared harness, and GI-4 §7 already recorded building one as a larger
change than the bug it would guard.

## 8. Risks

**8.1 This story widens the three-copy drift risk GI-4 already recorded.** GI-4 §8.1 documented the
peak window existing in `deepseek-peak-guard.sh`, `deepseek-auto-toggle.js` and
`internal/pricing.IsPeak`, unguarded against drift. GI-24 adds *a fourth divergence axis* — the
holiday calendar — and the plugin's two copies do not get it: after this story the guard will still
block a session on Thursday 2026-10-01 while lens correctly prices it off-peak. The two artifacts
will disagree in the user's face. *Mitigation*: nothing in this story fixes it, and pretending
otherwise would be worse than naming it. The plugin copies can only be corrected in their own repo
(their window is `2x` in a blocked-prompt message and a small predicate), and that is a follow-up
story, recorded here so it is a known cost rather than a surprise. The *practical* severity is
bounded: the guard blocks a session the user can immediately re-run off-peak, and lens's number
stays right.

**8.2 The shipped default is a dated fact that goes stale in 2027.** See D9. It fails in the
overstating direction, silently, and the only mitigation available is making it visible. *Residual*:
a user who never runs `lens doctor` gets 2027 priced with no holidays at all. A worse variant —
2026 dates reapplied in 2027 — cannot happen, because the dates carry their year.

**8.3 `config.Validate` now imports `pricing`.** Recorded in D2 with its rationale and the rejected
alternative. The concrete hazard is a future cycle if `pricing` ever needs `config`; `pricing`
imports only `parse` today and the plan adds no import to it.

**8.4 The 调休 semantics are the user's decision, not a documented fact.** The chosen design makes
them *expressible* and ships them as six 调休 work-day overrides. DeepSeek's published rule says "weekends and
public holidays" and never mentions 调休, so the shipped default asserts something no source
confirms. *Mitigation*: the default is data the user can edit in one line, the README cites the
State Council notice for the dates and says plainly that DeepSeek's own treatment of 调休 is
unverified, and the container the dates live in is the same one that would carry the correction.
This is the one place this plan knowingly encodes an unverified claim — it is called out here rather
than buried in a default string.

**8.5 What a schema change would have unlocked, recorded so the deferral is a choice.** A `peak`
column on `requests` would give the sessions *list* a peak column, the Stats tab a peak-vs-off-peak
spend split, and an indexable predicate — all without a Go loop. It is deferred because it requires
inventing a migration mechanism (§D6), not because the features are unwanted. The natural follow-up
story is the migration mechanism first, the column second.

**8.6 A holiday fix is forward-only.** See D8. The concrete trap is a user reading this story's
changelog and expecting their October 2026 rows to correct themselves. *Mitigation*: stated in the
README and here, not left to inference.

**8.7 Self-review lens** (required by the flywheel). *Security* — no auth, secret, permission or
network surface is touched. No new file is read at request time; the calendar comes from config,
which is already read at startup. The loopback binding, `x-api-key` redaction, and the three guarded
write routes are untouched. The one new API output (`sessionPeak`) exposes an aggregate over rows the
dashboard already serves in full. *QA* — the edge cases that matter are the grammar's malformed
shapes, the two-set contradiction, precedence in both directions, the UTC-keying boundary, the
unpriced-at-peak case in both the warning and the rollup, the zero-calendar regression, and doctor's
exit code on an uncovered year; all are enumerated in §7. *Architecture* — the cost step keeps its
place in the fixed pipeline order, the hot path gains nothing, no new *module* dependency appears (the
one new *intra-repo* import edge, `config → pricing`, is recorded in D2/§8.3), and the two new
*setter* installs (`Consumer.SetCalendar`, `api.SetCalendar`) go through the optional-setter seam
the repo already uses five times (`SetPricing`, `SetRetention` on `api`; `SetPriceTable`,
`SetSessionAggregator`, `SetBodyDecoding` on `consumer` — §3), while the third install point is the
compile-enforced `analyze.NewRules` argument (§D7).

## 9. Pre-flight (needs a decision before bead 01)

1. **Create issue `GI#24`.** Verified free, but GitHub shares one sequence between issues and PRs and
   the next story will face the same trap: check `gh issue list --state all` **against `gh pr list
   --state all`**, never against the highest issue number. This is the note GI-4 §9.1 left and it
   held again.
2. **Cut the branch** `GI-24-holiday-aware-peak-pricing` from `develop`, per `CLAUDE.md`'s branch
   policy. Every commit is prefixed `GI#24`.
3. **Re-verify the three ⚠ counts in §3 no later than bead 01** — §3 assigns that re-verification to
   bead 01 — and necessarily before **bead 02**, the first bead that edits a call site rather than
   adding a file (its `Compute` edits and `pricing.IsPeak` removal consume two of the three ⚠
   counts). A drifted count is this repo's most repeated review finding.
4. **Update `docs/context/` in the same PR** (Phase 5.6). This is not a skip, and the refresh is not
   the glob's to guess — but the file list lives in **§6's `docs/context/*.md` row, and is
   deliberately not restated here.** It was restated until v8, and the restatement is why this item
   was wrong twice: v7 corrected it by adding `testing-and-quality.md` and `cli-and-tooling.md` and
   in the same edit silently dropped `data-model.md` and `workflows.md`, which §6 still names. The
   same inventory written down twice drifted in opposite directions, which is the §3 count class
   again, one layer down — so read the list from §6 rather than from a copy that can diverge from
   it.

## 10. Beads

| # | Title | Priority | Depends | Files |
|---|---|---|---|---|
| 01 | `feat` pricing: the calendar type, date grammar, and holiday-aware window | P0 | — | `calendar.go`, `calendar_test.go` (new) — includes `Calendar.Covers(year)` for bead 07 and `Calendar.DateSets()`, which echoes the configured strings verbatim (bead 05's Settings display and bead 07's doctor print read it) |
| 02 | `feat` pricing: thread the calendar through `Compute` | P0 | 01 | `pricing.go`, `pricing_test.go`, `consumer.go`, `consumer_test.go` |
| 03 | `feat` config: `off_peak_dates` and `work_dates` | P0 | 01 | `config.go`, `config_test.go` |
| 04 | `feat` analyze: the holiday-aware `peak_pricing` warning | P0 | 01, 02 | `analyze.go`, `rules.go`, `kinds.go`, `analyze_test.go`, `README.md` |
| 05 | `feat` api+web: the per-session peak rollup | P0 | 01, 02 | `api.go`, `api_test.go`, `prices.go`, `prices_test.go`, `app.js`, `index.html` |
| 06 | `feat` cli: wire the calendar through `serve` and `replay` | P0 | 02, 03, 04, 05 | `serve.go`, `internal/cli/serve_test.go` (new), `internal/cli/replay_test.go`, `internal/replay/replay_test.go`, `internal/consumer/consumer_test.go` |
| 07 | `feat` cli: `doctor` reports the calendar and its coverage | P1 | 03, 06 | `doctor.go`, `cli_test.go` |
| 08 | `docs` lens: README, price-file header, and the Settings wording | P1 | 04, 05 | `README.md`, `table.go`, `index.html` |
| 09 | `docs` refresh `docs/context/` | P1 | 06, 07, 08 | `docs/context/*.md` |

One root, `01`. `01` fans out to `{02, 03}`; `02` and `03` both feed
`06`, which is the single wiring point and therefore the first bead whose absence is invisible in a
unit test; `04` and `05` are siblings over `{01, 02}` and touch disjoint files except for two shared
files, whose ownership is by *region* rather than by whole file: bead 04 owns the `README.md` warning
row, bead 05 owns the `index.html` drill-down markup **and the effective-calendar display** (it
renders the `/api/prices` `off_peak_dates` / `work_dates` fields bead 05 adds), and bead 08 owns the
prose in both (`README.md` peak-pricing prose + `index.html` Settings wording).
`consumer_test.go` is shared the
same way — bead 02 adds its holiday-priced row, bead 06 gives its `NewRules` call site (`:91`) the
calendar argument.

**The `NewRules` third-parameter change (bead 04) has 8 call sites — 4 inside `internal/analyze`**
(`analyze.go:89`'s `defaultRules` plus `analyze_test.go:524`/`:542`/`:555`), all owned by bead 04,
**and 4 outside** — `serve.go:85`, `internal/cli/replay_test.go:93`,
`internal/replay/replay_test.go:107`, and `internal/consumer/consumer_test.go:91` — and bead 06's
file list carries all four, so each is repaired inside the graph. The other transient break is bead
02 removing the package-level `pricing.IsPeak` that `rules.go:262` calls, which stops that one line
compiling until bead 04 rewrites it; that break *is* repaired in the graph, which is exactly what v2
failed to arrange for `consumer_test.go:91` — owned by bead 02, which runs before the signature
exists, and omitted from bead 04, which changes it. Maximum depth is six
(`01 → 02 → 04 → 06 → 07 → 09`).

**Suggested order**: `01 → 03 → 02 → 04 → 05 → 06 → 07 → 08 → 09`. Bead 03 before 02 because the
config keys are what make the calendar reachable, and a calendar nothing constructs cannot be
exercised end to end.

**Which beads the user actually feels.** 01, 02, 03 and 06 are the whole of the fix: correct pricing
on 19 weekdays a year. 04 is what explains a doubled number. 05 and 07 are what make the calendar's
*state* visible — which, given that the default goes stale and the 调休 rule is unverified, is not
decoration. 08 and 09 are the difference between a fix and a documented fix.

## Change History

- **v9** — post-convergence, documentation-only, made during beadification. §9 item 4 restated §6's
  `docs/context/*.md` inventory and had drifted from it: it named four files where §6 names six,
  and the two it dropped (`data-model.md`, `workflows.md`) are ones §6 still lists. v7 had already
  touched this item once, correcting the opposite omission (`testing-and-quality.md`,
  `cli-and-tooling.md`) — so the copy had now been wrong in both directions across two edits while
  the original stayed right. The fix is not a third re-sync: item 4 no longer restates the list and
  points at §6 instead, removing the second place the inventory can diverge. No design decision,
  file list, bead, dependency, or count elsewhere changes; bead 09 already enumerated all six files
  independently, so no bead is affected.
- **v8** — round-7 review triage. Made the calendar install-point accounting single-valued (F7.1).
  §D7's preamble claimed all three installs entail "no constructor signature changes" eleven lines
  before recording `analyze.NewRules` as the one constructor that must change; qualified it to "so
  neither `api.New` nor `consumer.New` changes signature — the sole signature change is
  `analyze.NewRules`' third argument". §8.7 said "both new install points go through the
  optional-setter seam" — the wrong count (three) and the wrong attribution (the `NewRules` install
  is a constructor argument, not a setter); now it names the two setter installs explicitly
  (`Consumer.SetCalendar`, `api.SetCalendar`) and attributes the third to the compile-enforced
  `analyze.NewRules` argument. No count in the five-setter seam changed.
- **v7** — round-6 review triage. Completed the `docs/context/` refresh enumeration: §6's row and §9
  item 4 named four files, leaving `testing-and-quality.md` (the `pricing.IsPeak` paragraph at
  `:66-71`) and `cli-and-tooling.md` (the `doctor` check list at `:10`) unowned though this plan
  invalidates both (F6.1). Reconciled §8.7's "no new dependency appears" with §D2/§8.3 by qualifying
  it to "no new *module* dependency" and naming the one new intra-repo edge, `config → pricing` (F6.2).
  Stated the rollup share's numerator/denominator asymmetry in §D6: `CostUSD` sums the newest ≤1000
  loaded rows while `Session.TotalCostUSD` aggregates *all* calls, so a >1000-call session's share
  undercounts even though `Calls` is exact (F6.3).
- **v6** — round-5 review triage. Gave the `wireCalendar` helper a compilable signature: its third
  parameter named `*api.API`, a type that does not exist (`api.New` returns the unexported `*api`,
  `api.go:68`/`:137`), so package `cli` could not name it — replaced with an interface parameter
  `interface{ SetCalendar(pricing.Calendar) }`, the lazy precedent-matching choice (F5.1). Added the
  `serve_test.go` wiring assertion to §7's `internal/cli` bullet, which carried the `doctor` cases
  only while §D7/§6/§10 named the file and §8.7 leaned on §7 as the complete enumeration (F5.2).
  Corrected §8.7's optional-setter count from "three times" to **five** (`api`'s `SetPricing`/
  `SetRetention`; `consumer`'s `SetPriceTable`/`SetSessionAggregator`/`SetBodyDecoding`), matching
  §3's own row (F5.3).
- **v5** — round-4 review triage. Corrected the reason the hardcoded `rules.go:266` sentence goes
  stale: it is false on a **调休 make-up weekend** the rule fires on, *not* on a holiday — an
  `OffPeakDates` holiday is off-peak all day (D3 rule 1), so `rulePeakPricing` never fires on one
  (§1, §6); retargeted §7's assertion to that 调休 Saturday so it is satisfiable (F4.1). Added the
  third `serve.go` install point — the `NewRules` third argument at `:85` — to §6's `serve.go` row,
  which named only the two setters (F4.2). Made bead 06's "wiring assertion" concrete: named the
  `wireCalendar` helper, the new `internal/cli/serve_test.go`, and what it asserts (F4.3). Pointed §9
  item 3 at bead 02, the actual first bead to edit a call site (bead 03 is the config bead), and
  reconciled it with §3's "bead 01 must re-verify" (F4.4).
- **v4** — round-3 review triage. Gave the calendar-to-`/api/prices` seam an owning mechanism: a
  `Calendar.DateSets()` accessor (bead 01) that echoes the two configured strings verbatim, read
  through the existing single-parameter `api.SetCalendar` seam and threaded into `renderPrices`
  (F3.1). Corrected the `consumer.New` count to its real total, **32** (27 in `consumer_test.go` +
  5 cross-package), with the cross-package set listed (F3.2). Reconciled the `NewRules` split to
  **4 internal / 4 external** in §D7, matching §10 (F3.3). Tagged §6's `index.html` edits with
  explicit owners — the calendar display and drill-down markup to bead 05, the Settings wording to
  bead 08 — and named the display region in §10's ownership prose (F3.4). Fixed the
  `SetBodyDecoding` citation to `consumer.go:127-151` (F3.5).
- **v3** — round-2 review triage. Gave the orphaned `NewRules` call site at
  `internal/consumer/consumer_test.go:91` a repair bead, with all four external call sites now in
  bead 06 (F2.1), and re-derived the §10 longest chain: **six**, not the four v2 stated nor the five
  the reviewer proposed (`01 → 02 → 04 → 06 → 07 → 09`) (F2.3). Corrected the §6 `prices.go` wire
  name `peak_dates` → `work_dates` (F2.2). Added the hardcoded `rules.go:266` detail-sentence fix to
  §6/§7 (F2.4). Qualified §3's "every" and stated the `DefaultLimit` cap's consequence in §D6, with
  the rejected alternative's global cap contrasted (F2.5). Replaced §D9's false "never reaches it"
  claim with the coverage check guarding its own `NewCalendar` construction (F2.6). Made §6's
  `Validate` call shape one `NewCalendar(offPeak, work)` so the both-sets rejection is reachable
  (F2.7). Specified the zero-denominator rendering and pinned it (F2.8). Unified the rollup field on
  `Calls` and corrected the `CostUSD` comment (F2.9).
- **v2** — round-1 review triage. Renamed the second config key `PeakDates` → `WorkDates` and
  rewrote D3 as the symmetric rest-day model (F1.1/F1.2), with the corrected D4 proof and the
  `2026-02-14 16:30 UTC` counterexample; kept the read-time rollup and documented its cross-calendar
  collision with the stored badge in D6/D8, rescoping D5's guarantee (F1.4); corrected the
  `SetCalendar` no-op consequences (F1.3); fixed the `NewRules` count 7 → 8 and added its two missing
  test call sites to §6/§10 (F1.5); added the `Calendar.Covers(year)` accessor for the doctor check
  (F1.6); corrected the `Validate`-not-`Load` failure point across D2/D9/§7 (F1.7); added the missing
  `internal/pricing/table.go` row (F1.8); fixed two citations (F1.9); and fixed the bead-root /
  file-ownership prose (F1.10). Also audited every stated count against its own evidence list — only
  the `NewRules` count was wrong.
- **v1** — initial plan after Phase 1 intake. Decisions taken with the user: two date sets (off-peak
  + 调休 work-day overrides); per-record warning retained and a per-session rollup added with no
  schema change; `PeakMultiplier` left hardcoded.
