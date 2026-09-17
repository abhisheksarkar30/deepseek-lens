# GI-17 — Price-rate tab, retention, and unpriced purge

**Ticket**: [#17](https://github.com/abhisheksarkar30/deepseek-lens/issues/17) ·
**Branch**: `GI-17-pricing-retention-purge` (cut from `develop`) ·
**Plan version**: v10 ·
**Status**: converged

## Problem

Three gaps around what lens knows about money and how long it keeps anything.

1. **Price rates are only editable from the CLI.** `lens prices --set/--unset/--edit` writes
   `~/.deepseek-lens/prices.toml` and `pricing.Loader` re-reads it live, so the mechanism works —
   but the dashboard, where every cost is actually *displayed*, has no pricing surface at all. A row
   showing `unpriced` gives the user no way to fix it without leaving the browser and recalling the
   `model.field=rate` syntax.
2. **Nothing expires.** `store.PurgeOlderThan` is implemented, tested (session reconciliation
   included) and called by nothing — dead code from br-GI-1-06. No retention key, no schedule, no
   command, no route.
3. **Unpriced rows accumulate permanently.** A row stored before its model's rate was configured
   keeps `cost_usd IS NULL` forever, so an all-time total is understated by exactly those rows and
   nothing can clear them.

## Scope and the interpretations made

In scope: a dashboard tab that shows and edits the price table; opt-in retention by days with a
preview of what would be deleted; a dedicated purge for unpriced records; a CLI for both purges.

Two interpretation calls, both flagged because the request allowed more than one reading:

- **"retention number of days options" is satisfied by a config/flag/env setting that the UI
  *shows*, plus one-off purges from the UI — not by the dashboard rewriting `config.toml`.**
  Rewriting that file would have to reconstruct a hand-editable document whose comments and
  unrecognized keys `parseFlatFile` drops on the way in; corrupting the file that also holds
  `ProxyAddr` and `DBPath` is a bad trade for a value that already has three inputs. The UI gives
  the outcome (purge what you want, now) without the destructive write. **D12**.
- **One new tab named "Settings" holding two sections**, not two tabs. Prices and retention are both
  "configure lens"; the tab bar is sticky and already four items wide. The price table is the first
  section, so "a tab for showing/editing the price rates" is satisfied literally. **D13**.

## Design decisions

**D1 — The pricing write path is a file write, not shared in-memory state.**
`POST /api/prices` does `pricing.Load(path)` → replace one model's `Rates` → `pricing.Save(path, tbl)`.
The consumer's `pricing.Loader` picks the change up on its next stat (`Table()` re-reads on
mtime-or-size change, [table.go:243-269](internal/pricing/table.go#L243-L269)), so a rate set in the
browser prices the next request with no restart and with no invalidation protocol. The dashboard and
`lens prices` then write the same file through the same two functions — one code path, not two.

**`Save` becomes atomic (temp file + rename).** Today `Save` is `os.WriteFile(path, …, O_TRUNC)`
([table.go:183](internal/pricing/table.go#L183)), and the consumer calls `Loader.Table()` once per
captured call ([consumer.go:429-434](internal/consumer/consumer.go#L429-L434)). A stat+read landing
between the truncate and the write sees an *empty* file; `parseTable("")` returns an empty table with
no error ([table.go:81-114](internal/pricing/table.go#L81-L114)), so `Load` merges `Default()` and
that call is stored with `cost_usd IS NULL` **permanently** — the exact row class this ticket exists
to eliminate, created by the very feature meant to fix it. So `Save` writes a *uniquely named* temp
file in the target's directory — `os.CreateTemp(filepath.Dir(path), ".prices-*.tmp")`, removed on the
error path — `f.Sync()`s it, then `os.Rename`s it over the target (one extra create and one rename
per write). The temp name must be unique per writer, not the fixed `path + ".tmp"`: R1 accepts
concurrent CLI-vs-dashboard saves, and two writers sharing one temp inode (both opening it `O_TRUNC`)
could interleave their bytes before either renames, leaving a mixed file that may not parse or may
parse into a wrong table — strictly worse than R1's documented last-writer-wins. `os.CreateTemp`'s
unique name removes that at no extra cost (still one create + one rename). The rename is atomic, so a
concurrent `Loader` read sees either the whole old file or the whole new one, never a truncated or
empty one. This closes the window for the new browser writer *and*
for the pre-existing `lens prices --set` path. Change detection is unaffected: the renamed file carries
a fresh mtime/size, so `Loader` re-reads.

*Ceiling*: two processes (a `lens prices` invocation and `serve`) racing a read-modify-write lose one
update (last writer wins). Single-user, hand-edited file; accepted and documented in the route's
comment. Atomicity removes partial/empty reads; it does not remove the lost-update race.

**D2 — `api.New` keeps its signature shape and gains `SetPricing`/`SetRetention` seams; it returns
`*api`.** `serve.go` already installs optional capabilities this way three times
(`cons.SetSessionAggregator`, `SetPriceTable`, `SetBodyDecoding`) precisely so a feature bead does not
rewrite an existing constructor and its callers. Threading the new seams through the constructor as
more positional parameters — the pricing path, the retention days, and the purge surface — would be
worse than a return-type change. `*api` satisfies `http.Handler` via a `ServeHTTP` method delegating to the
`*http.ServeMux` it now stores, so `Handler: api.New(...)` in [serve.go:110](internal/cli/serve.go#L110)
still compiles.

`SetPricing(path string)` carries the price-file path the handler loads from and saves to (D1's
`pricing.Load(path)`/`Save(path, …)`) — a path, not an in-memory table, so the handler cannot
accidentally bypass the file the consumer's `Loader` re-reads.

`SetRetention(days int, purge RetentionPurger)` carries the configured days *and* the purge surface:
a small interface of the store methods the purge/preview handlers call. The handlers serve **two**
previews over **two** predicates, so the read methods must be named for both — `PurgeOlderThan`,
`PurgeUnpriced` (the writes), plus `CountPurgeable(ctx, cutoff) (count int, oldest, newest *time.Time,
err error)`/`PurgeableBytes(ctx, cutoff)` for the older-than preview and
`CountUnpriced(ctx)`/`UnpricedBytes(ctx)` for the D5 preview. `CountPurgeable` carries the older-than
range (`MIN`/`MAX(started_at)` over the same `started_at < cutoff` rows) so the preview's
`oldest`/`newest` have a named carrier in the seam, not merely a source — a bare `Count*`/`*Bytes`
pair cannot produce them. Both are **nullable** (`*time.Time`, `nil` when the eligible count is `0`),
because `MIN`/`MAX` over an empty eligible set is SQL `NULL` and cannot scan into a value `time.Time`
— the same trap `reconcileSession` avoids by scanning into `sql.NullInt64`
([store.go:917-924](internal/store/store.go#L917-L924)). Two read methods named for one predicate, as
an earlier draft had them, cannot produce both `eligible_*` and `unpriced_*` pairs.
So `api.Store` — documented as "the narrow *read*
slice of `*store.Store`" ([api.go:27-45](internal/api/api.go#L27-L45)) — stays read-only, and the
purge write path enters through the seam rather than by widening that interface (or by contradicting
its doc comment). An unwired `SetRetention` is what makes both retention routes answer 503.

**D3 — Price route shape: one model per POST, whole row, `null` means unset.**
`{"model":"deepseek-flash","rates":{"input":0.28,"output":null,"cache_read":null,"cache_write":null}}`.
Whole-row-per-model matches how the UI edits ("this row, saved"), and `null` is exactly what
`lens prices --unset` means — reverting to a bare model line, which is what keeps *known but
unpriced* distinct from *unknown-model* ([table.go:148-152](internal/pricing/table.go#L148-L152)).
A field-level PATCH would need a third syntax; a whole-table PUT would clobber a concurrent
`$EDITOR` session. Adding a model absent from the table is allowed — that is how a user fixes
`unknown-model`, and `parseTable` already accepts arbitrary valid model names.

**D4 — Input validation moves to the persistence boundary, and the model-name check is *tightened*.**
`Save` writes a flat file with no escaping: `{"model":"x\nfoo.input = 1"}` would inject a line that
`parseTable` then reads back as a legitimate entry. The hole already exists on the CLI
(`lens prices --set $'evil\nx.input=1'` writes two parseable lines) — this story adds a *browser* as
an input source, which makes it worth closing properly. So: export the model-name check and one rate
check from `internal/pricing`, use them from `parseTable`, from `cli.applySet`, and from `Save`
itself. One guard covering every writer by construction is smaller than three copies of the check.

`validModel` must be **tightened, not merely exported**. It currently accepts `.`
([table.go:139-141](internal/pricing/table.go#L139-L141)), but `Save` writes `model + "." + field`
([table.go:176](internal/pricing/table.go#L176)) and `parseTable` cuts at the **first** `.`
([table.go:99-101](internal/pricing/table.go#L99-L101)) — so a model named `a.b` writes
`a.b.input = 0.28`, which reads back as model `a` / field `b.input` and fails the whole `Load`. Remove
`.` from the accepted rune set (which already rejects `=`, whitespace and newline), so no model name
can round-trip into a broken file. Known behaviour change: a previously-accepted bare line like
`deepseek.v2` ([table.go:90-97](internal/pricing/table.go#L90-L97)) newly becomes a parse error, which
fails `Load` and drops the `Loader` to its last-good table. Accepted and stated in `validModel`'s
comment — no real DeepSeek model name contains `.`, and such a line cannot round-trip through `Save`
anyway.

The two checks report a *typed* rejection so a caller can map it to a status. Export sentinels
`pricing.ErrInvalidName` and `pricing.ErrInvalidRate`, wrapped with `%w`; the I/O failures stay plain
wrapped errors ([table.go:155-157](internal/pricing/table.go#L155-L157),
[:183-185](internal/pricing/table.go#L183-L185)). `POST /api/prices` then uses `errors.Is` — 400 for
the two sentinels, 500 for a disk failure — instead of mapping every `Save` error to 400 (a full disk
is not a bad request).

An unknown rate field never reaches `Rates.Set`: `POST /api/prices` decodes its body with
`json.Decoder.DisallowUnknownFields()` (`rates` decoded as a struct of the four known fields), so an
unknown field is rejected as a malformed body — a 400 at decode, before any validation or write.
That is what makes the contract's "unknown field" a 400 rather than a `Save`-level 500.

**D5 — "Unpriced" is a strict subset of the `unpriced` cost-source bucket plus a positive-token
conjunct.**
`cost_usd IS NULL` alone is a **superset** of "unpriced": it also matches every `unknown-model` row,
because `Compute` returns `Cost{Source: SourceUnknownModel}` with a nil `Amount`
([pricing.go:140-148](internal/pricing/pricing.go#L140-L148)), and the dashboard presents
`unknown-model` and `unpriced` as distinct groups. An irreversible "purge unpriced" driven by the bare
NULL test would silently destroy a class of rows the user was never shown, and the preview's one
number would hide it. So the predicate starts from the `COALESCE` condition `StatsByCostSource`
already uses to label a row `unpriced`: `COALESCE(cost_source, 'unpriced') = 'unpriced'`, which folds
in the NULL-source rows (its `COALESCE` maps them to `unpriced`) while leaving `unknown-model`,
`configured` and `approximate` as their own groups. The citation is to the grouping and the NULL case
only — `StatsByCostSource`'s doc comment names the NULL-source bucket, not `unknown-model`, and its
`COALESCE` keeps `unknown-model` separate
([store.go:527-540](internal/store/store.go#L527-L540)). Note the precedence: a bare `<>` test would
be NULL-safe-false and wrongly drop the NULL-source rows, which is why the `COALESCE` form is used.

**The positive-token conjunct makes this a strict subset of the Stats tab's `unpriced` group, not an
equality with it.** `StatsByCostSource` groups on `COALESCE(cost_source,'unpriced')` with **no** token
condition ([store.go:533-540](internal/store/store.go#L533-L540)), so the Stats tab's `unpriced N`
counts every NULL-cost row including zero-token error/4xx responses; the D5 predicate
(`COALESCE(...)='unpriced' AND tokens > 0`) deliberately excludes those zero-token rows. So the two
on-screen numbers are *different by design*: `GET /api/retention`'s `unpriced_requests` is the
positive-token subset of the Stats tab's `unpriced` group, and they differ by exactly the zero-token
rows. The conjunct is what makes the purge *"records of actual tokens used"*: without it, error and
4xx responses that were never priceable would be destroyed too, which is not what was asked. (It is a
subset — never a superset — of the Stats group: same `COALESCE` bucket, extra token condition.) There
is no `tokens` column, so the conjunct is spelled out over the schema's four token columns
([schema.sql:23-26](internal/store/schema.sql#L23-L26)). The full predicate is:

```sql
COALESCE(cost_source, 'unpriced') = 'unpriced'
  AND (input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) > 0
```

`GET /api/retention`'s `unpriced_requests`/`unpriced_bytes` and the purge both use this exact
predicate, held in one place (a shared `store` constant + `purgeUnpricedWhere` helper), so the preview
count and the delete predicate agree by construction and the `unknown-model` bucket is never touched.
The preview and the delete agree **exactly**; it is the preview and the *Stats tab* that deliberately
differ by the zero-token rows. The Settings-tab confirm copy must say so — the unpriced action's
"would delete N" is the positive-token count, so a user comparing it to the Stats tab's larger
`unpriced N` reads the label, not a contradiction.

**D6 — One purge implementation, two predicates, one result type.**
`PurgeOlderThan` and `PurgeUnpriced` differ only in their WHERE clause. Extract
`purgeWhere(ctx, where string, args ...any) (PurgeResult, error)` holding the transaction, the
warnings delete, and the per-affected-session `reconcileSession` loop; both public methods become thin
wrappers over it. The session-reconciliation logic is the part most likely to be fixed in one copy and
forgotten in the other, so keeping exactly one copy is the point.

Both public methods return the same result type:

```go
// store.PurgeResult — exported because it crosses into internal/api.
type PurgeResult struct {
	Deleted            int64 // requests removed
	SessionsReconciled int   // distinct sessions touched (len(affected) at store.go:893)
}
```

`PurgeOlderThan(ctx, cutoff) (PurgeResult, error)` and the new `PurgeUnpriced(ctx) (PurgeResult, error)`
both return it. The second count cannot be dropped from the contract: `POST /api/purge` returns
`sessions_reconciled` for **both** modes, and only the loop that walks `affected`
([store.go:893-897](internal/store/store.go#L893-L897)) knows how many sessions were touched — a
`(int64, error)` method cannot report it, so a handler serving `sessions_reconciled` for the
older-than mode is unimplementable against that shape. `PurgeOlderThan`'s two existing call sites
([store_test.go:582](internal/store/store_test.go#L582), `:657`) are updated to read `res.Deleted`
(a same-package change), and they still assert the same deleted count and the same session
reconciliation.

**D7 — A deliberate second writer, and CLAUDE.md amended to say what is actually true.**
CLAUDE.md states SQLite's only writer is the consumer goroutine. A purge necessarily runs off that
goroutine — it is triggered by a schedule and by a button. There are **two** purge writers, and the
amended invariant must name both honestly, because R6/Phase 6.5 publishes this wording into
`CLAUDE.md` and a claim its own D9 contradicts would be a documented lie:

- **In-process purge** (the scheduled/startup run and `POST /api/purge`) uses the store's writer
  connection, which is `SetMaxOpenConns(1)` ([store.go:105](internal/store/store.go#L105)), so it
  queues behind ingest rather than racing it. The package doc already calls the single-writer
  discipline "belt and braces, not the primary guarantee".
- **`lens purge`** is a *separate process with its own writer connection* (D9). It is **not**
  serialized by the in-process `SetMaxOpenConns(1)`; it is serialized cross-process by WAL plus
  `busy_timeout(5000)` ([store.go:43](internal/store/store.go#L43)) — the same protocol every other
  external opener relies on.

So the honest invariant is: *row ingest has exactly one writer; purges are the other writers —
in-process ones sharing the single writer connection, and the `lens purge` process serialized
cross-process by WAL + `busy_timeout`* — rather than invent a command channel for the consumer to
keep a stricter sentence literally true. Do **not** publish a "the purge path is serialized through
the same single connection" claim: it is false for `lens purge`. A new store test runs a concurrent
purge and insert under `-race` and asserts what such a test can actually assert: both calls return
nil, and every row is accounted for — present
or deleted, none lost or duplicated. It is **not** a race detector for those two calls: `s.writer` is
capped at one open connection ([store.go:105-106](internal/store/store.go#L105-L106)), so
`database/sql` serializes the calls before any Go-memory race can exist, and `*sql.DB` is
concurrency-safe by contract. The serialization guarantee is `SetMaxOpenConns(1)`'s, not the test's;
the test pins the no-lost-row behaviour that guarantee is meant to produce. (Cf.
`TestReaderDoesNotBlock` ([store_test.go:523-556](internal/store/store_test.go#L523-L556)) for this
repo's shape of a contention test that *can* fail — a reader on a timeout against an open writer tx.)

**Amending the invariant means amending *every* statement of it — search, not sample.**
A source comment left asserting "the consumer stays the only writer" after `lens purge`
ships is the same falsified claim as a stale doc line — and leaving them is the
sample-vs-class defect this loop has now re-found three rounds running (F1.3 docs → F5.2
docs → F6.1 *source comments*). So the amendment is driven by a **governing search** per
changed invariant. Three rules make each search a real gate:

1. **The gate is on the claim-bearing hit set, not the topic word.** A topic-word search
   (`read-only`, `one writer`) also matches *true* statements and other tickets' frozen
   records, so "the result set is empty" is unreachable and would force the implementer to
   falsify a true sentence or reword the *fix* to satisfy the *grep*. The patterns are
   therefore written in **assertion form** — they match the falsified *claim*, not the bare
   topic — and the bead is done when the set of **claim-bearing** hits is empty.
2. **Every search carries a positive control.** Before an empty claim-bearing result counts,
   the search must be shown to find at least one *known* pre-change hit, named inline below. A
   search that returns nothing *before* the change is a broken search, not a converged tree —
   e.g. `rg` is Rust regex (ERE): `|` alternates, while `\|` is a **literal pipe** and matches
   nothing, so a `\|` in a pattern silently no-ops the gate (a zero-hit search satisfies
   "empty" by accident, reading as enforced while enforcing nothing). A search with no
   demonstrated hit is not a gate and must not be written as one.
3. **Frozen records are excluded by an explicit rule.** Other tickets' plans
   (`docs/planning/GI-*.md` — including *this* plan, which quotes the old wording while
   describing the change), merged specs (`docs/superpowers/specs/**`), and prior tickets' beads
   (`.beads/GI-1/**`, `.beads/GI-4/**`, `.beads/GI-15/**`) are historical records of what was
   true for *those* tickets and **MUST NOT** be rewritten to satisfy a search — e.g.
   `docs/planning/GI-15-pagination.md:155` states the single-writer invariant and **stays**.
   Excluding them is deliberate, not an oversight.

#### Governing searches (whole tree, frozen paths excluded)

Each search is run with `rg`, assertion-form, with the frozen-path exclusions
(`-g '!docs/planning/GI-*' -g '!docs/superpowers/specs' -g '!.beads/GI-1' -g '!.beads/GI-4'
-g '!.beads/GI-15'`), and its positive control asserted before the empty claim-bearing result
is accepted.

**1. "SQLite has exactly one writer: the consumer goroutine" / single-writer.**

```
rg -n -i -g '!docs/planning/GI-*' -g '!docs/superpowers/specs' \
      -g '!.beads/GI-1' -g '!.beads/GI-4' -g '!.beads/GI-15' \
      -e 'exactly one goroutine' \
      -e 'only the consumer goroutine' \
      -e 'ever calls a writer method' \
      -e 'consumer (stays|remains) the (only|sole) writer' \
      -e "the consumer's single writer" \
      -e 'sqlite has (exactly )?one writer' \
      -e 'must not become a second writer'
```

- *Positive control*: `internal/store/store.go:4` ("exactly one goroutine ever calls a writer
  method") must match before any edit.
- *Declared true lines that stay* (excluded from the claim-bearing set — **not** amended): `store.go:75`
  ("opened with one writer connection" — the connection cap, true; not matched), `consumer.go:94` (the
  consumer's single `Run` goroutine — true; not matched), `consumer_test.go:498/:546` (struct-field
  ownership — true; not matched), `docs/context/architecture.md:46` (the `SetMaxOpenConns(1)` config —
  true; not matched), `docs/context/data-model.md:103-104` (the `SetMaxOpenConns(1)` / WAL connection
  facts — true; the claim-bearing lines of that same section are `:105-:107`, which the pattern *does*
  match and amend), `docs/context/testing-and-quality.md:72` (the `TestConcurrentInsertsSerialize`
  label — true; not matched), and `api.go:326/:354` ("the consumer's single writer" describing replay
  traffic — true for replay, and *matched* by the pattern, so it must be named here to stay).

**2. "The API is read-only" / "the one write route".**

```
rg -n -i -g '!docs/planning/GI-*' -g '!docs/superpowers/specs' \
      -g '!.beads/GI-1' -g '!.beads/GI-4' -g '!.beads/GI-15' \
      -e 'read-only (json )?api' \
      -e '(the|our) (api|dashboard|json api) is read-only' \
      -e 'the one (credentialless )?write (route|guard)' \
      -e 'the one state-changing route' \
      -e 'the only .{0,12}(write|state-changing) route'
```

- *Positive control*: `internal/api/broker.go:1` ("the read-only JSON API") must match before
  any edit.
- *Declared true lines that stay*: `internal/consumer/analyzer.go:29` and
  `internal/session/session.go:77` ("read-only" aggregates / resolution — true, and unrelated to
  the HTTP surface).

**3. "`lens` has eleven subcommands".**

```
rg -n -i -g '!docs/planning/GI-*' -g '!docs/superpowers/specs' \
      -g '!.beads/GI-1' -g '!.beads/GI-4' -g '!.beads/GI-15' \
      -e 'eleven (sub)?commands?' -e 'all eleven names' -e 'every name here is implemented'
```

- *Positive control*: `docs/context/architecture.md:50` ("Eleven subcommand implementations")
  and `cmd/lens/main.go:13` ("Every name here is implemented") must match before any edit.
- *Declared true lines that stay*: `internal/cli/format.go`'s generic "subcommand" mentions (the
  word, not the count — not hits under this count-claim pattern). Cross-checked against
  `cmd/lens/main.go`'s dispatch map, which gains its twelfth entry (`purge`).

Ownership goes to the bead whose change falsifies the claim:

- **05** owns the **single-writer** search (search 1): `store.go`'s package doc (the `store.go:4`
  positive control) and every claim-bearing hit — including `internal/api/api.go:455-456`, the
  `sendReplay` doc that quotes `CLAUDE.md`'s single-writer sentence. That line is the
  *single-writer* claim; the read-only owners (02/04) do **not** own it.
- **06** — the scheduled purge. **07** — `POST /api/purge`.
- **08** — `lens purge`, the second *process*: it falsifies `replay.go`'s "the consumer stays
  the only writer" prelude and `replay_test.go`'s "must not become a second writer" comments;
  and it **owns the subcommand-inventory search** (search 3), because it registers the twelfth
  subcommand — `cmd/lens/main.go`'s dispatch map and its "Every name here is implemented"
  prelude, plus the count statements the search finds (`docs/context/architecture.md:50`
  "Eleven subcommand implementations", `docs/context/cli-and-tooling.md:6` "All eleven names
  are implemented").
- **02/04** — `internal/api`'s **read-only / one-write-route** comments: search 2's own hits here
  are `api.go:67`, `broker.go`'s package doc, and `api_test.go:83` (the test preamble's "these tests
  cover the read-only API"); `api.go`'s `:78/:103/:335` wrap the claim across source lines, so
  line-based `rg` does not see them and they are named explicitly in the Modify row below, not
  bounded by the search. Search 2's remaining claim-bearing hit is `internal/cli/replay.go:207`
  ("reads one request through the read-only API"), outside `internal/api` — amended by the bead that
  changes the read-only-API invariant, alongside the rest of the search's claim-bearing set.
- **11** — `CLAUDE.md` (the amended invariant), `README.md`, and `docs/context/` for the
  single-writer and read-only searches.

The searches over-match on purpose — a stray "read-only" is a hit to *read*, not to skip; the
*source of truth* is the writer connection in `internal/store`, the route registrations in
`internal/api`, and `cmd/lens/main.go`'s map.

**D8 — Retention is a threshold, not a stored job.** `retention_days` default `0` = keep forever
(matching this repo's fail-open posture: nothing is deleted on an unconfigured install). When > 0,
serve purges at startup and then on a 24h ticker, logging each run's deleted count.

**D9 — `lens purge` opens the store directly, so it works with no server running.**
`lens replay` deliberately goes through the running server's route to avoid a second writer. Purge
takes the opposite path on purpose: the usual reason to purge is to reclaim disk, and that is
exactly when you would rather the proxy were not running. WAL plus `busy_timeout(5000)` serializes
the two processes safely (see D7 — the CLI process is serialized cross-process, not by the in-process
writer connection). Unlike the read-only commands, `lens purge` cannot use `openStore`: that helper
calls `config.Load(nil)` and returns only the `*store.Store`
([format.go:220-226](internal/cli/format.go#L220-L226)), throwing away the resolved config — and
`lens purge` needs the configured `retention_days` (as the default for `--older-than`).

`lens purge`'s own flags (`--older-than`, `--unpriced`, `--dry-run`, `--yes`, `--vacuum`, bead 08) are
**not** `lens` config flags, so they must never be passed to `config.Load`. `applyFlags` registers only
the config flag set with `flag.ContinueOnError` ([config.go:214-231](internal/config/config.go#L214-L231))
and `Load` returns its parse error ([config.go:259-261](internal/config/config.go#L259-L261)), so
`config.Load(args)` on `--unpriced` aborts with *flag provided but not defined* before the store ever
opens. The house convention for a subcommand with non-config flags is `config.Load(nil)` — exactly what
`Replay` does ([replay.go:38](internal/cli/replay.go#L38)) before parsing its own flags in its own
`flag.FlagSet` ([replay.go:56-72](internal/cli/replay.go#L56-L72)). So `lens purge` calls
`config.Load(nil)` to read `DBPath` + `RetentionDays`, parses its five flags in its own `FlagSet`, then
`store.Open(cfg.DBPath)`.

**CLI validation — the default must not be a delete-everything.** `--older-than <days>` computes
`cutoff = now − days`, and `retention_days` defaults to `0` (D8), so `--older-than 0` would make the
predicate `started_at < now`, which matches **every** row ([store.go:857/860/884](internal/store/store.go#L857));
a *negative* `days` is worse still — `cutoff = now + |days|` also matches every row. The route
already refuses a non-positive `days` (contract below); the CLI, which reaches `store.Open` directly
and bypasses that route, must enforce the same guard. **Every surface uses the one threshold:
`retention_days <= 0` means "retention not configured"** — the route's `days <= 0` guard, R2's
requirement, and the CLI guard below read the same bound, never a mix of `== 0` / `<= 0` / `> 0`.

- Explicit `--older-than <days>` requires `days >= 1`, with the same message the route uses for a
  non-positive `days`; a `days <= 0` (zero **or** negative) is refused.
- When **neither** predicate flag is given, `--older-than` is the default, using the configured
  `retention_days`; the implied default is applied **only when `retention_days >= 1`**. When
  `retention_days <= 0` ("keep forever" — zero or negative; nothing configured to purge) the command
  refuses and points the user at `--older-than <days>` or `--unpriced`, so a negative or zero
  configured value can never produce an implied cutoff and a whole-table delete.
- **`--older-than` and `--unpriced` are mutually exclusive.** Passing both is a user error: the
  command exits non-zero with a message naming both flags and stating that they select different
  predicates (e.g. "`--older-than` and `--unpriced` select different rows and cannot be combined").
  The two predicates are never OR-ed into one WHERE clause and never run as two sequential purges —
  the command refuses before `store.Open`, so nothing is deleted.
- `--dry-run` still applies the same validation before it previews; `--yes` gates the write, not the
  guard.
- `--vacuum` runs `(*Store).Vacuum(ctx)` — a `VACUUM` executed on the store's writer connection,
  which the CLI reaches through the `*store.Store` `store.Open` returns (the `writer` field itself is
  unexported, so this is the only path). `VACUUM` needs free space on the order of the DB size and
  takes an exclusive lock, so it is opt-in and never automatic (R3): `--dry-run` reports what a purge
  *would* delete and skips the vacuum entirely.

**D10 — One purge route, two modes.** `POST /api/purge` with `{"mode":"older_than","days":N}` or
`{"mode":"unpriced"}`, both behind the same `replayOriginReject` allowlist the replay route uses.
The guard's rejection strings are replay-specific today ("replay requires a loopback Host …", "replay
rejected cross-origin request …", [api.go:604-625](internal/api/api.go#L604-L625)), so reusing them
verbatim would answer a rejected `POST /api/purge`/`POST /api/prices` with a body about *replay*,
which reads as the wrong endpoint having been hit. So parameterize the action name (or return a
reason code and let each handler phrase its own message), and have the mirrored guard test assert the
right action in the body for each route, not just a 403.
Two modes on one route because the guard, the confirm and the "report what was deleted" response are
identical; only the WHERE clause differs.

**D11 — Preview is a read; deletion is a write.** `GET /api/retention` returns the configured days
and a preview — eligible request count, approximate reclaimable bytes, oldest and newest eligible
timestamps — and deletes nothing, so the confirmation the user clicks is informed by a real number.
When `retention_days <= 0` ("keep forever") the older-than preview is *empty* by definition — its
counts and byte sum are `0` and `oldest`/`newest` are `null` — and the Settings tab shows the "keep
forever" state rather than an older-than purge action. `eligible_*` must never be computed from
`cutoff = now`, which would present the whole table as eligible and put a "delete everything" button
one click away under the *safest* setting. The unpriced preview does not depend on `days` and reports
either way, so the unpriced action remains available when retention is off.

**D12 — Setting retention persistently stays a flag/env/file action.** The dashboard displays the
configured value and offers one-off purges. See *Scope* above.

**D13 — One new tab, "Settings", with a Pricing section and a Data section.** The `views` array and
`showView` gain one entry.

**D14 — The accessibility rules in [conventions.md](../context/conventions.md) carry over.** A glyph
never carries meaning alone (`costBadge` already supplies the unpriced `?` with a `title` and
`aria-label`), interpolated text is escaped before it reaches an attribute, and anything revealed by
a click scrolls itself into view.

**D15 — Web changes are verified the way GI-15 did it.** `node --check`, then assert the new markup
appears in the bytes the *running server* returns (not the files on disk — `internal/web` is
`go:embed`-ed, so a binary built before the edit keeps serving the old assets), then a manual eyeball
for the interactive parts, with a throwaway extract-and-run script for the editor's own state logic.

## What changes

### Create

| File | Why |
|---|---|
| `internal/api/prices.go` | `GET`/`POST /api/prices` handlers |
| `internal/api/prices_test.go` | Chart shape, set/unset, validation, guard, unwired 503 (`GET`) |
| `internal/api/purge.go` | `GET /api/retention`, `POST /api/purge` handlers |
| `internal/api/purge_test.go` | Preview-vs-delete, both modes, guard, unwired state |
| `internal/cli/purge.go` | `lens purge` subcommand |
| `internal/cli/purge_test.go` | Dry-run deletes nothing; `--yes` gate; both predicates; the `days >= 1` / keep-forever / mutually-exclusive-flags guards (D9); `--dry-run` skips `--vacuum` |

### Modify

| File | Change |
|---|---|
| [internal/pricing/table.go](internal/pricing/table.go) | Tighten + export the model-name and rate checks; validate in `Save`; atomic temp+rename save; `ErrInvalidName`/`ErrInvalidRate` sentinels (D1, D4) |
| [internal/pricing/pricing_test.go](internal/pricing/pricing_test.go) | Rejection tests (sentinels) for injected model names and bad rates; atomic-save test |
| [internal/cli/prices.go](internal/cli/prices.go) | `applySet` drops its inline `rate < 0` check and its own `strings.Cut` model handling and calls the exported `pricing` checks — the "one guard, not three copies" goal of D4, currently optional because no row owned it; `--set`'s rejection message comes from the shared check |
| [internal/cli/prices_test.go](internal/cli/prices_test.go) | `--set` rejection-message assertions track the shared check |
| [internal/api/api.go](internal/api/api.go) | Store the mux; `ServeHTTP`; `New` returns `*api`; `SetPricing(path string)`/`SetRetention(days int, purge RetentionPurger)` (the retention seam carries the purge methods, so `api.Store` stays the read slice); parameterize `replayOriginReject`'s action name (D10); register 4 routes; amend the read-only / one-write-route comments (`:67/:78/:103/:335` — `:67` is search 2's only `api.go` hit; `:78/:103/:335` wrap across source lines and are amended as the explicitly named lines, not as search hits — and `broker.go`'s package doc) per the D7 read-only search, and the single-writer quote in `sendReplay`'s doc (`:455-456`) per the D7 single-writer search (owned by bead 05, not the read-only owners) |
| [internal/api/api_test.go](internal/api/api_test.go) | `newTestAPI` returns `*api` so new tests can reach `SetPricing`/`SetRetention` |
| [internal/store/store.go](internal/store/store.go) | Extract `purgeWhere`; `PurgeOlderThan`/`PurgeUnpriced` return `PurgeResult`; add the two preview reads — `CountPurgeable(ctx, cutoff) (count int, oldest, newest *time.Time, err error)`/`PurgeableBytes(ctx, cutoff)` for older-than and `CountUnpriced(ctx)`/`UnpricedBytes(ctx)` for the D5 predicate (each pair sharing its purge's predicate; both byte reads `COALESCE(SUM(...), 0)` so an empty set yields `0`, and `CountPurgeable` scans `MIN`/`MAX` into a nullable type so an empty set yields `nil`) — and `(*Store).Vacuum(ctx) error` (runs `VACUUM` on the writer connection; D9, R3); amend the package doc (`:4-8`) to name the purge writers (D7) |
| [internal/store/store_test.go](internal/store/store_test.go) | Unpriced-purge cases; `PurgeResult` return updates; concurrent purge+insert under `-race` |
| [internal/store/types.go](internal/store/types.go) | `PurgeResult` (internal carrier, no json tags — `api` owns the tagged wire shape) |
| [internal/config/config.go](internal/config/config.go) | `RetentionDays` + `LENS_RETENTION_DAYS` + `--retention-days` + `Validate` |
| [internal/config/config_test.go](internal/config/config_test.go) | Precedence and rejection cases |
| [internal/cli/doctor.go](internal/cli/doctor.go) | `retention_days` in the effective-config table |
| [internal/cli/serve.go](internal/cli/serve.go) | Wire the seams; startup purge + daily ticker |
| [internal/cli/cli_test.go](internal/cli/cli_test.go) | Startup-purge coverage |
| [cmd/lens/main.go](cmd/lens/main.go) | Register `purge`; amend the "Every name here is implemented" dispatch-map prelude for the twelfth subcommand (D7 subcommand-inventory search, owned by bead 08) |
| [internal/web/index.html](internal/web/index.html) | Settings tab button + `#view-settings` two sections |
| [internal/web/app.js](internal/web/app.js) | Tab wiring, price table render/edit/save, purge panel |
| [internal/web/style.css](internal/web/style.css) | Editable-cell and destructive-action styles |
| `CLAUDE.md` | Amend the single-writer invariant to name **both** purge writers honestly (D7: in-process purge on the single writer connection; `lens purge` a separate process serialized by WAL + `busy_timeout`) and the one-way data-flow bullet (a purge is `api → store`); note the three write routes. Do not publish a "same single connection" claim for the CLI process |
| `README.md` | Retention, purge, and the Settings tab for users |
| Every file the D7 governing searches hit **as a claim-bearing match** for a changed invariant | Amend the assertion so the search's post-change **claim-bearing** hit set is empty — source comments *and* docs, owned by the bead that changes the invariant (D7's ownership note), with the search's positive control asserted first. The search bounds the set, not this row: claim-bearing members include `internal/store/store.go` (package doc), `internal/cli/replay.go`, `internal/cli/replay_test.go`, `internal/api/api.go`, `internal/api/broker.go`, and `cmd/lens/main.go`'s dispatch map + prelude. The search's **declared true lines** and the **frozen paths** (other tickets' plans/specs/beads) are **not** amended |

## Contracts

### `GET /api/prices`

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

`null` is an unset rate, never an implicit zero — the same distinction `Rates`' pointer fields carry
([pricing.go:69-74](internal/pricing/pricing.go#L69-L74)). `source` is `Rates.Source()`. Models are
sorted. 503 when no price path is wired.

### `POST /api/prices`

Body: `{"model":<string>,"rates":{<field>:<number|null>,...}}`, fields from `input`, `output`,
`cache_read`, `cache_write`. Replaces that one model's rates wholesale; an omitted field is unset.
Returns the same shape as `GET` so the UI re-renders from what was actually written.

- 400 — invalid model name, unknown field, negative/NaN/Inf rate, or malformed body. An unknown rate
  field is rejected at decode (`json.Decoder.DisallowUnknownFields()`, D4) — a malformed-body 400
  before `Rates.Set` is reached; the name/rate rejections are
  `pricing.ErrInvalidName`/`ErrInvalidRate` and are matched with `errors.Is` (D4).
- 500 — `Save` failed with anything else (a disk/write error is not a bad request).
- 403 — `replayOriginReject` (non-loopback `Host`, or cross-origin).
- 503 — pricing not wired.

### `GET /api/retention`

```json
{"days":30,"cutoff":"2026-08-17T00:00:00Z","eligible_requests":412,
 "eligible_bytes":52428800,"oldest":"2026-07-01T09:12:00Z","newest":"2026-08-16T23:58:00Z",
 "unpriced_requests":37,"unpriced_bytes":1048576}
```

Both previews in one response because the tab shows both actions at once.
`eligible_bytes` is `COALESCE(SUM(LENGTH(req_body)+LENGTH(resp_body)), 0)` over the eligible rows — a
scan of the eligible set, not a page of it. The `COALESCE` is load-bearing: `SUM` over zero rows is
SQL `NULL`, which cannot scan into the `int64` the response carries, and zero eligible rows is a
supported state, not a corner (a `days` larger than the data's age, or the default `days <= 0`).
*Ceiling*: linear in eligible rows; fine at this tool's scale. Approximate: a row whose
`req_body`/`resp_body` is `NULL` contributes nothing to the sum (`LENGTH(NULL)` is `NULL` and `SUM`
skips it), so body-less rows are undercounted.

`days <= 0` (the default, "keep forever"): `eligible_requests` and `eligible_bytes` are `0`,
`oldest`/`newest` are `null`, and `cutoff` is **`null`** — no threshold is configured, so nothing is
eligible and there is no instant to name; `cutoff` is never a zero timestamp and never an echo of
`now`. `null` is the same "unset" convention D3 uses for a rate, and the Settings tab renders it as
such (no cutoff shown, the "keep forever" state). For `days > 0`, `cutoff = now − days` and the
eligible set is `started_at < cutoff`; `cutoff` is then that instant as an RFC3339 string. A `days`
larger than the data's age is that same empty-set state **with `cutoff` set**: `eligible_requests` and
`eligible_bytes` are `0` and `oldest`/`newest` are `null` — `oldest`/`newest` are `null` **whenever
the eligible count is `0`**, not only when `days <= 0`.

`unpriced_requests`/`unpriced_bytes` are computed from the **exact D5 predicate** — the same
`COALESCE(cost_source,'unpriced')='unpriced' AND (four token columns) > 0` the purge deletes on — so
the preview class and the delete class agree by construction, and the `unknown-model` bucket is never
counted or deleted. `unpriced_bytes` is `COALESCE(SUM(LENGTH(req_body)+LENGTH(resp_body)), 0)` over
those rows (same empty-set `COALESCE` as `eligible_bytes`, since an install with no unpriced rows is
the ordinary case, not a corner).
This is a strict **subset** of the Stats tab's `unpriced` group (which has no token condition and so
also counts zero-token rows) — the two numbers differ by exactly those rows, by design; they are not
expected to be equal and must not be presented as the same figure. (`unpriced_*` do not depend on
`days`; they report whether or not retention is configured.) The
response is built by a small tagged struct in `internal/api`, not by serving a `store` type directly —
`internal/store` carries no json tags on purpose ([types.go:57-61](internal/store/types.go#L57-L61)).

### `POST /api/purge`

Body: `{"mode":"older_than","days":30}` or `{"mode":"unpriced"}`; `days` is ignored for `unpriced`.
Returns `{"mode":...,"deleted":<n>,"sessions_reconciled":<n>}` — `deleted` is `PurgeResult.Deleted`
and `sessions_reconciled` is `PurgeResult.SessionsReconciled` (D6), for both modes. Destructive: 400
on a missing or unknown mode, or a non-positive `days`. A `days` value larger than the data's age is
**not** an error — it deletes nothing and returns `deleted: 0`. 403 via `replayOriginReject`
(parameterized by action, D10). Never a GET.

## Test strategy

**Unit — `internal/pricing`**
`Save` rejects a model name containing a newline, `=`, `.`, or a space, and writes nothing on
rejection; the rejection is `ErrInvalidName` (`errors.Is`) and a bad rate is `ErrInvalidRate`; a rate
of NaN/±Inf/negative is likewise rejected by the shared `ErrInvalidRate`-wrapped check; `0` is
accepted (a genuinely free model, distinct from unset); a round trip through `Save`/`Load` preserves
unset fields and a bare-model line. `Save` is atomic: it writes a temp file in the target's directory
and renames it over the target, leaving no `.tmp` behind and never exposing a truncated file.
`parseTable` rejects a dotted bare model line (`deepseek.v2`) — the D4 tightening.

**Unit — `internal/config`**
`RetentionDays` resolves flag > env > file > default; default is `0`; `Validate` rejects a negative.

**Unit — `internal/store`**
`PurgeUnpriced` deletes rows matching the D5 predicate (`COALESCE(cost_source,'unpriced')='unpriced'`
and positive total tokens), and *keeps* priced rows, keeps `unknown-model` rows (the `COALESCE` groups
them separately from `unpriced`), and keeps unpriced rows with zero tokens (an error response is not
"tokens used"). `PurgeResult.Deleted` counts only deleted requests and `SessionsReconciled` the distinct
sessions touched. Affected sessions are reconciled and a session left with no rows is deleted.
`PurgeOlderThan`'s existing tests are updated to the `PurgeResult` return and still assert the same
deleted count and reconciliation across the `purgeWhere` extraction. A concurrent purge + `InsertRequest`
test runs under `-race` and asserts both calls succeed with no row lost or duplicated — the test's
guarantee is `SetMaxOpenConns(1)`'s, not the race detector's (D7). Purging an empty match returns
`PurgeResult{}, nil` and touches no session row. `CountPurgeable`/`PurgeableBytes` (older-than) and
`CountUnpriced`/`UnpricedBytes` (D5) each return exactly the rows and bytes *their* delete acts on —
the older-than reads use the same `started_at < cutoff` predicate as `PurgeOlderThan`, the D5 reads
the same `COALESCE(...) AND tokens > 0` predicate as `PurgeUnpriced` — so each preview agrees with its
delete and the two pairs are not interchangeable. Over an empty eligible set (a cutoff older than
every row, or no unpriced rows) `CountPurgeable` returns `count 0` with `oldest`/`newest` `nil`,
`PurgeableBytes`/`UnpricedBytes` return `0` (not an error), and `CountUnpriced` returns `0`.

**Unit — `internal/api`**
`GET /api/prices` renders `null` for unset rates and `unpriced` for a model with no input rate.
`POST` sets a rate, unsets with `null`, adds a previously unknown model, rejects a name with a
newline in it with 400 without modifying the file, and rejects an unknown rate field with 400
(decoded-time, D4); a rejected name/rate maps to 400 via the `ErrInvalidName`/`ErrInvalidRate`
sentinels, while a `Save` I/O failure maps to 500 (a wrapped non-sentinel error). `POST /api/prices`
and `POST /api/purge` both
reject a cross-origin `Origin` and a non-loopback `Host` with 403 — mirrored from replay's guard
test, because a second write route with a weaker guard is the failure this story exists to avoid.
`GET /api/retention` reports counts and deletes nothing (assert the row count is unchanged after
calling it), and its `unpriced_requests` equals what the purge actually deletes; with `days = 0` the
eligible counts are `0`, `oldest`/`newest` and `cutoff` are `null`, and no `cutoff`-wide set is
offered; with `days > 0` `cutoff` is the RFC3339 instant `now − days` and the eligible set is
`started_at < cutoff`; with `days > 0` but **zero** eligible rows (a `days` larger than the data's
age) the response is a 200 with `eligible_requests`/`eligible_bytes` `0` and `oldest`/`newest`
`null`, not an error — and likewise an install with no unpriced rows answers `unpriced_requests`/
`unpriced_bytes` `0`. Both write routes answer 503 when unwired, and so do both read routes:
`GET /api/prices` answers 503 with its price path unwired (the case the `prices_test.go` row names)
and `GET /api/retention` answers 503 with the retention seam unwired (the `purge_test.go` unwired
case).

**Unit — `internal/cli`**
`lens purge --dry-run` deletes nothing; `--yes` is required to write. The guards (D9): `--older-than 0`
and `--older-than -1` are refused with the route's non-positive-`days` message and delete nothing; with
`retention_days = 0` and no explicit `--older-than`, the implied default is refused rather than run as
a whole-table delete, and a **negative** `retention_days` (`-5`) is refused the same way — a
non-positive configured value can never produce an implied cutoff; `--older-than <days> --unpriced`
together is refused with a message naming both flags (they select different predicates) and deletes
nothing — never OR-ed into one predicate and never run as two sequential purges; `--unpriced` alone is
unaffected by the days
guards. Each guard test
asserts the store was left untouched (row count unchanged). `--dry-run` skips `--vacuum` — no `VACUUM`
runs.

**Integration — the two that matter**
1. *Price takes effect without a restart*: `POST /api/prices` against a temp path, then a real
   `pricing.NewLoader` on that same path prices the next request at the new rate. This is D1's
   premise; a naive implementation that mutated only an in-memory copy fails here and nowhere else.
2. *Retention end to end*: with `retention_days` set, a serve startup purges exactly the rows past
   the cutoff, and the `sessions` totals afterwards agree with the rows that remain — the assertion
   that catches a purge that deleted rows without reconciling.

**Web** — `node --check internal/web/app.js`; assert the Settings tab and the new section markup
appear in the bytes `/app.js` and `/` actually serve; manual eyeball for edit-save-reload and the
purge confirm; a throwaway extract-and-run script (not committed, per the existing no-JS-harness
convention) for the editor's dirty-state logic. The unpriced confirm copy labels its number as the
positive-token unpriced count (the strict subset of the Stats tab's `unpriced` group), so the two
on-screen figures are not read as a contradiction (D5).

**Commands**: `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./internal/store/`.

## Risk areas

| # | Risk | Mitigation |
|---|---|---|
| R1 | Lost update on `prices.toml` when the CLI and the dashboard write concurrently | Accepted, documented on the route; the UI re-renders from the response, so the user sees what actually landed (D1) |
| R2 | A purge is irreversible | Preview with real counts first; `--yes` required in the CLI **and** an explicit positive `--older-than` (or a configured `retention_days >= 1`) so the default is never a whole-table delete — a `retention_days <= 0` means retention is not configured and the CLI refuses the implied default rather than deleting every row (D9); a confirm step in the UI; retention off by default; the unpriced purge is a separate, explicitly-named action |
| R3 | **The `.db` file does not shrink after `DELETE`** — SQLite reuses freed pages but keeps the file size, which undercuts the main reason to purge | Report file size before/after, and offer `lens purge --vacuum` as an explicit opt-in, backed by `(*Store).Vacuum(ctx)` (a `VACUUM` on the store's writer connection — the CLI cannot reach it otherwise, D9). `VACUUM` needs free space on the order of the DB size and takes an exclusive lock that blocks ingest, so it is never automatic and never in the dashboard, where a multi-second freeze with no feedback is a bad experience |
| R4 | A first purge over a large table holds the writer connection and queues ingest | One transaction for atomicity — batching would leave a half-purged database on failure, which is worse than slow. The eligible count is shown before the click so the wait is not a surprise |
| R5 | Dashboard totals and the feed describe rows that no longer exist | The purge response drives an explicit reload of the active view; no new SSE event type is invented for it |
| R6 | `CLAUDE.md`, the context docs, **and the in-source comments** currently claim a read-only API with one write route and a single SQLite writer | Bead 11 amends `CLAUDE.md` twice (the single-writer invariant per D7 **and** the "Data flow is strictly one-way … store → api → web" bullet a purge violates — `api → store`, reading no rows first). The refresh is driven by D7's **governing searches** (assertion-form, whole-tree, frozen paths excluded), not a hand-listed sample: the sample-vs-class defect recurred as F1.3 (docs) → F5.2 (docs) → F6.1 (**source comments**), so the plan names the searches and requires each search's **claim-bearing** hit set to be **empty** — the *topic-word* set is **not** required to be empty (true lines and frozen records stay), and each search carries a **positive control** (a known pre-change hit: `store.go:4`, `broker.go:1`, `architecture.md:50`) so a pattern that matches nothing by accident cannot read as a converged tree (D7). Every claim-bearing hit — doc **or** source comment — is updated by the bead that changes the invariant (D7's ownership note). The doc hits earlier rounds named (`architecture.md` `:46/:48/:50/:54`, `cli-and-tooling.md` `:6`, `security-and-permissions.md` `:33-35/:39/:44/:64`, `data-model.md` `:103-107`, `api-surface.md` `:28/:70/:95`, `INDEX.md`'s conditional table + "Last generated / refreshed" stamp) and the in-source hits F6.1 found (`store.go` `:4-8`, `replay.go` `:30-36`, `api.go` `:455-456`, `replay_test.go` `:236-239`) are members of the class, not its bounds; the declared true lines (`store.go:75`, `consumer.go:94`, `consumer_test.go:498/:546`, `architecture.md:46`, `testing-and-quality.md:72`, `api.go:326/:354`, the `internal/consumer/analyzer.go`/`session.go` "read-only" comments) and the frozen other-ticket paths stay |
| R7 | `internal/web` has no JS test runner, so editor logic is verified by a throwaway script only | The same ceiling GI-15 accepted and documented; keep the editor's state minimal (dirty flag + one save call) so there is little logic to be wrong |

## Self-review

**As a senior engineer.** The seams (`SetPricing`/`SetRetention`) follow the pattern `serve.go`
already uses three times, so no existing constructor call is rewritten beyond a return type. The
pricing write path deliberately avoids new state: the file *is* the interface, and `Loader`'s
existing stat-based re-read is the propagation mechanism, so there is nothing to invalidate. The
`purgeWhere` extraction removes a duplication whose two copies would have to stay in sync on the
subtle part (session reconciliation). The one thing I would push back on if reviewing: `api.New`
changing its return type is a wider blast radius than a setter-free alternative would be — the
alternative is threading the pricing path, the retention days and the purge surface through as more
positional parameters, or an `Options` struct that rewrites every test
call site. Of the three, the return type is the smallest diff and matches the existing idiom.

**As a QA engineer.** Boundary cases are enumerated above: zero-token unpriced rows survive (the
predicate's second conjunct exists for exactly that, and makes `unpriced_requests` a strict subset of
the Stats tab's `unpriced` group); setting a rate to `0` is allowed and distinct
from `null`; unsetting only the input rate is allowed and downgrades the model to `unpriced` while
leaving other rates set; adding a new model is allowed; a `days` larger than the data's age is a
no-op rather than an error; a purge matching nothing returns `PurgeResult{}, nil` and reconciles
nothing; with `retention_days = 0` the older-than preview is empty rather than "everything", and the
CLI refuses an implied `--older-than` for the same reason. Error
scenarios: malformed JSON, unknown field name, unknown mode, unwired seams. The reordering hazard —
preview saying one number and the delete taking another, because the table changed in between — is
inherent to a preview and acceptable here; the response reports what was *actually* deleted rather
than what was previewed.

**As a security engineer.** Two new write routes appear on a listener whose whole justification for
having no auth is that it is read-only and loopback-bound: `POST /api/prices` (edits the price file)
and `POST /api/purge` (destructive). They join the one existing write route, `POST
/api/requests/{id}/replay`, so the listener now carries **three** write routes total, one of them
destructive. Every one
reuses `replayOriginReject`, tested for both rejection arms, so a cross-site form POST from any page
the user happens to have open is turned away by the Origin/Host comparison rather than by luck. No
route performs an action on GET. The price-file injection vector (D4) is closed at the one place
every writer passes through, which also fixes the pre-existing CLI instance. `GET /api/prices`
discloses the local file path — already printed by `lens prices` and `lens doctor`, and this tool is
loopback-only, so it is not new exposure. Rate values are validated finite and non-negative, matching
`parseRate`'s existing rule, so a `1e999` cannot become `+Inf` and poison every subsequent total.
Purging removes `req_body`/`resp_body`, which are the sensitive payloads; nothing about the purge
copies them anywhere first, and no preview returns body content — only counts, byte sums and
timestamps.

## Beads

Written to `.beads/GI-17/` in Phase 3. Outline:

| # | Title | Depends on |
|---|---|---|
| 01 | Tighten + export pricing validation at the save boundary (`ErrInvalidName`/`ErrInvalidRate`); atomic save; remove `applySet`'s inline duplicate check so the CLI and `Save` share the one guard (D4) | — |
| 02 | Give the dashboard API write-route seams (`*api`, `SetPricing(path)`, `SetRetention(days, purger)`); parameterize the origin guard's action name | — |
| 03 | `GET /api/prices` — the effective price table | 02 |
| 04 | `POST /api/prices` — set/unset a model's rates behind the origin guard | 01, 02, 03 |
| 05 | `store.purgeWhere` extraction → `PurgeResult`, `PurgeUnpriced`, both preview reads (`CountPurgeable` (count + nullable `oldest`/`newest`)/`PurgeableBytes`; `CountUnpriced`/`UnpricedBytes`; both byte reads `COALESCE(SUM(...), 0)`), `(*Store).Vacuum`, and the `store.go` package-doc amendment — owns the D7 **single-writer search** (assert its positive control `store.go:4`, then amend the claim-bearing hits: the `store.go` package doc and `internal/api/api.go:455-456`; `store.go:75` "one writer connection" is a declared true line that stays) | — |
| 06 | `retention_days` config, doctor output, and the scheduled purge | 05 |
| 07 | `GET /api/retention` and `POST /api/purge` | 02, 05 |
| 08 | `lens purge` subcommand: `--older-than <days>` (defaults to configured `retention_days`; requires `days >= 1` and refuses an implied default when `retention_days <= 0`, so a negative or zero configured value never produces an implied cutoff), `--unpriced`, `--dry-run` (skips `--vacuum`), `--yes`, `--vacuum` (runs `(*Store).Vacuum`); `--older-than` and `--unpriced` are mutually exclusive — passing both is refused with a message naming both flags (D9); registers `purge` in `cmd/lens/main.go`'s dispatch map and amends its "Every name here is implemented" prelude for the twelfth subcommand — owns the D7 **subcommand-inventory search** (assert its positive control `architecture.md:50`, then amend every count statement it finds: `docs/context/architecture.md:50` "Eleven subcommand implementations", `docs/context/cli-and-tooling.md:6` "All eleven names are implemented"); amends `replay.go`'s "the consumer stays the only writer" prelude and `replay_test.go`'s "must not become a second writer" comments, which the new CLI writer falsifies (D7 single-writer search) | 05, 06 |
| 09 | Settings tab: the price table, editable in place | 03, 04 |
| 10 | Settings tab: retention and purge controls | 07, 09 |
| 11 | Docs: CLAUDE.md invariant amendment, README, context-doc refresh — run the D7 **single-writer** and **read-only/write-route** searches (positive controls `store.go:4` / `broker.go:1`) over the whole tree and require the **claim-bearing** set empty (frozen paths and declared true lines excepted); the refresh is the searches' claim-bearing hits, not a hand-listed subset. (The subcommand-inventory search is owned by bead 08.) | 01–10 |

## Deferred

- **Auto-pricing historical rows.** A rate set today could in principle re-price old rows that were
  stored unpriced. Not in scope: `cost_usd` is a recorded fact about what the call cost *at the
  time*, and rewriting it would silently change historical totals. The unpriced purge is the honest
  alternative — remove what cannot be priced rather than invent a price for it.
- **A dashboard editor for `retention_days`** (D12) — needs a config writer that preserves comments.
- **Per-model retention** and a size-based cap: YAGNI until days-based retention proves insufficient.

## Change History

**v2** — round-1 review fixes (F1.1–F1.13). The unpriced predicate is now the
`unpriced` cost-source bucket (`COALESCE(cost_source,'unpriced') = 'unpriced'`) plus the four-column
positive-token conjunct, shared verbatim between `GET /api/retention` and the purge; `Save` writes
atomically via temp-file + rename; the model-name check is tightened (rejects `.`) with typed
sentinels; `purgeWhere` returns a `PurgeResult`; the concurrency test is reframed to what it can
assert; the CLI purge flags and the preview `days <= 0` behaviour are named; the route/self-review
counts are corrected; and the doc-refresh list is the whole claim-bearing class.

**v3** — round-2 review fixes (F2.1–F2.10). `lens purge` now calls `config.Load(nil)` (like `Replay`)
and parses its own flags, instead of passing non-config flags to `config.Load(args)` (F2.1); the CLI
enforces a `days >= 1` guard and refuses an implied `--older-than` when `retention_days = 0` (F2.2);
the retention seam names a read method per predicate, so both preview pairs are producible (F2.3);
D5 is reworded — `unpriced_requests` is a strict *subset* of the Stats tab's `unpriced` group
(zero-token rows excluded), equal to the delete class but deliberately not to the Stats figure (F2.4);
D7 and the `CLAUDE.md` row distinguish the in-process purge (single writer connection) from the
separate `lens purge` process (WAL + `busy_timeout`), and drop the false "same single connection"
claim (F2.5); `Save` writes a uniquely named temp file (`os.CreateTemp`) rather than a fixed
`path + ".tmp"` (F2.6); the `POST /api/purge` contract no longer lists an over-age `days` as both a 400
and a no-op (F2.7); `replayOriginReject` is parameterized by action so a rejected purge/prices request
does not answer with a replay message (F2.8); `SetPricing(path string)`'s argument is stated (F2.9);
and the two conflicting positional-parameter counts are replaced by an enumeration (F2.10).

**v4** — round-3 review fixes (F3.1–F3.4). `--vacuum` gets an implementation path: `(*Store).Vacuum(ctx)`
added to the store modify row and beads 05/08, its mechanism stated in D9 (a `VACUUM` on the writer
connection `store.Open` returns), and `--dry-run` skips it (F3.1); `CountPurgeable` now returns the
older-than range (`oldest`/`newest`) so the seam has a named carrier for the fields
`GET /api/retention` advertises (F3.2); per the conductor override, the `GET /api/retention` contract
states explicitly that `cutoff` is JSON `null` when `days <= 0` (never a zero timestamp, never an echo
of `now`) and the RFC3339 instant when `days > 0`, with both branches covered in the test strategy
(F3.2 override); the `POST /api/prices` 400-for-unknown-field is backed by
`json.Decoder.DisallowUnknownFields()` at decode, so it never surfaces as a `Save`-level 500 (F3.3);
and R6's "Bead 10/11" is corrected to "Bead 11" (F3.4).

**v5** — round-4 review fixes (F4.1–F4.3). The empty eligible set is now handled at every read
(F4.1): `eligible_bytes`/`unpriced_bytes` are `COALESCE(SUM(LENGTH(req_body)+LENGTH(resp_body)), 0)`
(the house idiom, `store.go:403-409`), `CountPurgeable`'s `oldest`/`newest` are nullable
`*time.Time` (nil when the count is `0`; the same nullable-`MIN`/`MAX`-scan trap `reconcileSession`
avoids at `store.go:917-924`), and the contract states `oldest`/`newest` are `null` **whenever the
eligible count is `0`**, not only when `days <= 0` — with the empty-set case (a `days` larger than the
data's age, and an install with no unpriced rows) added to the `internal/store` and `internal/api`
test lists and a one-word note that `NULL`-body rows are undercounted by the approximate byte figure.
Bead 08 now depends on `06` as well as `05`, because `lens purge` reads the `RetentionDays` field
bead 06 introduces (F4.2). Per the conductor override, `--older-than` and `--unpriced` are mutually
exclusive: passing both is a user error that exits non-zero with a message naming both flags and
stating that they select different predicates — never a combined OR and never two sequential runs —
stated in D9 alongside the neither-given case and covered in the CLI test strategy and bead 08
(F4.3 override).

**v6** — round-5 review fixes (F5.1–F5.3). Per the conductor override, every surface now shares one
threshold — **`retention_days <= 0` means "retention not configured"** (F5.1): D9's CLI guard, the
route's `days <= 0` guard, and R2's requirement all read `<= 0`, replacing the previous mix of `== 0`
/ `<= 0` / `> 0`; on top of that, `lens purge`'s implied default is applied only when
`retention_days >= 1`, an explicit `--older-than` requires `days >= 1`, and a negative
`retention_days` is refused rather than computing `cutoff = now + |days|` (which matches every row) —
so a negative configured value can never produce an implied cutoff. The overrides are obeyed: `lens
purge` does **not** call `cfg.Validate()` and `Validate()` is **not** moved into `config.Load` (that
would make read-only commands like `lens stats` reject a `ProxyAddr`/`BodyPolicy` they never use). The
negative-retention_days case is added to the `internal/cli` test strategy and bead 08. R6 now states
the rule — refresh every doc line that states the route set, the subcommand set, the write-route
count, or the single-writer invariant — and adds the previously-missed members it and round-1 F1.3
shared the class of: `architecture.md:50` (the "Eleven subcommand implementations (…)" row),
`cli-and-tooling.md:6` ("All eleven names are implemented"), and `security-and-permissions.md:44`
("the one credentialless write guard in the system") with its `:33-35` capability table (F5.2). The
`internal/api` test list now names the unwired-503 case for both **read** routes — `GET /api/prices`
specifically, which the contract already required but no test row carried — alongside the two write
routes, and the `prices_test.go` create-row lists it (F5.3).

**v7** — round-6 review fixes (F6.1, F6.2) under a conductor override that generalizes them.
Per the override, the plan stops enumerating hits and enumerates the **class**: every invariant
the story changes — the single-writer discipline, the HTTP route inventory, the subcommand
inventory, and the read-only/write-route claim — now has a **governing search** (D7) whose
post-change result set must be **empty**, run over the whole tree (source comments *and* docs),
with the bead that changes the invariant owning its search and updating every hit; the Modify
table and bead lists now **cite the search**, not a sample of its results (F6.1 + override). The
in-source sites the round-6 review surfaced — `store.go`'s package doc ("exactly one goroutine
ever calls a writer method"), `replay.go`'s "the consumer stays the only writer" prelude,
`api.go`'s `sendReplay` doc quoting the rewritten CLAUDE.md sentence verbatim, `consumer.go`'s
"single-writer discipline", and `replay_test.go`'s "must not become a second writer" comments —
are named as members of the class, not its bounds; `store.go`, `api.go`, `replay.go`, and
`cmd/lens/main.go` carry comment amendments, and a catch-all Modify row plus bead owners
(05/06/07/08/02/04/11) carry the rest. F6.2: `internal/cli/prices.go` (drop `applySet`'s inline
`rate < 0` check and its own `strings.Cut` model handling in favour of D4's exported checks) and
`internal/cli/prices_test.go` join the Modify table, and bead 01 owns the dedup.

**v8** — round-7 review fixes (F7.1–F7.3), all under conductor overrides. **F7.1 (BLOCKER)**: the
three D7 governing searches were written with BRE alternation `\|`, but `rg` is Rust regex (ERE)
where `\|` is a *literal pipe* and matches nothing — the gate read as enforced while enforcing
nothing (a zero-hit search satisfies "empty" vacuously). Every search is rewritten with real `|`
alternation (rendered outside markdown-table cells, so a raw `|` is no longer escaped), and each
search is now paired with a **positive control** — a *known* pre-change hit named inline
(single-writer: `internal/store/store.go:4`; read-only: `internal/api/broker.go:1`; subcommand:
`docs/context/architecture.md:50` + `cmd/lens/main.go:13`) that must match before an empty result
counts; a search with no demonstrated hit is not a gate. **F7.2 (MAJOR)**: the unsatisfiable
"result set is empty over the whole tree" gate is replaced. D7 now states three rules —
(a) each search is **assertion-form** (it matches the falsified *claim*, not the bare topic);
(b) each search **declares a set of true lines that stay** as non-hits (`store.go:75`,
`consumer.go:95`, `consumer_test.go:498/:546`, `architecture.md:46`, `testing-and-quality.md:72`,
`api.go:326/:354`, the `analyzer.go`/`session.go` "read-only" comments); (c) the bead's gate is
that the **claim-bearing** hit set is empty, *not* the topic-word set. **Frozen paths are excluded
by an explicit stated rule** — other tickets' plans (`docs/planning/GI-*.md`), merged specs
(`docs/superpowers/specs/**`), this ticket's own plan, and prior tickets' beads (`.beads/GI-1/**`,
`-4/**`, `-15/**`) are historical records that MUST NOT be rewritten (e.g.
`docs/planning/GI-15-pagination.md:155` stays). `store.go`'s new wording ("row ingest has exactly
one writer; purges are the other writers") is a non-hit under the tightened pattern, so the
replacement no longer re-trips its own gate; R6, the catch-all Modify row, and beads 05/08/11
carry the same gate. **F7.3 (MINOR)**: the ownership gaps are closed — bead **08** now owns the
**subcommand-inventory search** (it registers the twelfth subcommand in `cmd/lens/main.go`'s
dispatch map and amends its "Every name here is implemented" prelude, plus the count statements
`architecture.md:50` and `cli-and-tooling.md:6`); and `internal/api/api.go:455-456` (the
`sendReplay` doc quoting the single-writer sentence) moves **off** the read-only owners (02/04)
**onto** bead 05, the bead that rewrites that invariant.

**v9** — round-8 review fixes (F8.1–F8.3), all citation corrections inside D7's governance
appendix; no governance semantics change. **F8.1**: the declared read-only non-hit cited a file that
does not exist — `internal/analyze/` holds no `analyzer.go` (its files are `analyze.go`, `kinds.go`,
`rules.go`, and two test files) and no "read-only" comment; the `// It is deliberately read-only …`
comment lives at `internal/consumer/analyzer.go:29`, and both the search-2 declared-stay citation and
the R6 shorthand now say so. **F8.2**: the search-2 ownership note overstated the search's reach —
search 2 as written matches only `api.go:67` and `broker.go`'s package doc in `internal/api`, while
`api.go:78/:103/:335` wrap the claim across source lines, which line-based `rg` cannot see; the note
and the `api.go` Modify row now name those three as explicitly listed lines, not search hits (the
enumerated list, not the search, is what fixes them — the *bounds* claim is corrected, the class
mechanism is unchanged). **F8.3**: the search-1 declared stay mislabels the consumer's
single-`Run`-goroutine line — the `// New and run it with Run; there is exactly one Run goroutine per
Consumer,` comment is `consumer.go:94` (`:95` is the `// matching the store's single-writer
discipline (CLAUDE.md).` line); the citation is corrected to `:94` in D7 and R6. This is the loop's
final round; the plan is marked **converged** in the header.

**v10** — round-9 review fix (F9.1, under conductor override), a citation correction inside D7's
governance appendix; no governance semantics change. The search-2 ownership note under-counted the
search's own hit set: search 2 as written also matches `internal/api/api_test.go:83` ("these tests
cover the read-only API") within `internal/api` and `internal/cli/replay.go:207` ("reads one request
through the read-only API") outside it, neither of which the note named or declared a stay. The
02/04 bullet now enumerates `api_test.go:83` among the `internal/api` hits and names
`replay.go:207` as the search's other claim-bearing hit, amended by the bead that changes the
read-only-API invariant. The plan is marked **converged** in the header (final version).
