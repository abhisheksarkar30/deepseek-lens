# Bead br-GI-17-01: Validation at the persistence boundary, and an atomic `Save`

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Design decisions D1, D4

- **Priority**: P0 (critical)
- **Dependencies**: none
- **Blocks**: br-GI-17-04

## Description

Two defects in `internal/pricing` that this ticket's browser writer would otherwise turn from
latent into reachable, plus the CLI cleanup that falls out of fixing them properly.

**1. `validModel` accepts `.`, and that cannot round-trip.** `Save` writes `model + "." + field`
([table.go:176](../../internal/pricing/table.go#L176)) and `parseTable` cuts at the **first** `.`
([table.go:99-101](../../internal/pricing/table.go#L99-L101)). So a model named `a.b` writes
`a.b.input = 0.28`, which reads back as model `a` / field `b.input` — an unknown field, so the whole
`Load` fails and the `Loader` drops to its last-good table. Remove `.` from `validModel`'s accepted
rune set, which already rejects `=`, whitespace and newline.

*Known behaviour change, stated rather than discovered:* a previously-accepted bare line like
`deepseek.v2` ([table.go:90-97](../../internal/pricing/table.go#L90-L97)) newly becomes a parse
error. Accepted and recorded in `validModel`'s comment — no real DeepSeek model name contains `.`,
and such a line cannot round-trip through `Save` anyway.

**2. The checks are package-private and applied in only some places.** Export the model-name check
and one rate check, and call them from all three writers: `parseTable`, `cli.applySet`, and `Save`
itself. `Save` is the one that matters — it writes a flat file with no escaping, so
`{"model":"x\nfoo.input = 1"}` injects a line `parseTable` then reads back as a legitimate entry.
The hole already exists on the CLI (`lens prices --set $'evil\nx.input=1'` writes two parseable
lines); this ticket adds a *browser* as an input source, which is what makes it worth closing at the
one place every writer passes through rather than in three copies.

**3. Typed rejections.** Export `pricing.ErrInvalidName` and `pricing.ErrInvalidRate`, wrapped with
`%w`. The I/O failures stay plain wrapped errors
([table.go:155-157](../../internal/pricing/table.go#L155-L157),
[:183-185](../../internal/pricing/table.go#L183-L185)). This is what lets `POST /api/prices` answer
400 for a bad name or rate and 500 for a disk failure, instead of mapping every `Save` error to 400
(a full disk is not a bad request).

**4. `Save` becomes atomic.** Today it is `os.WriteFile(path, …, O_TRUNC)`
([table.go:183](../../internal/pricing/table.go#L183)), and the consumer calls `Loader.Table()` once
per captured call ([consumer.go:429-434](../../internal/consumer/consumer.go#L429-L434)). A
stat+read landing between the truncate and the write sees an **empty** file; `parseTable("")`
returns an empty table with no error
([table.go:81-114](../../internal/pricing/table.go#L81-L114)), so `Load` merges `Default()` and that
call is stored with `cost_usd IS NULL` **permanently** — the exact row class this ticket exists to
eliminate, created by the very feature meant to fix it.

So: write a *uniquely named* temp file in the target's directory —
`os.CreateTemp(filepath.Dir(path), ".prices-*.tmp")`, removed on the error path — `f.Sync()` it,
then `os.Rename` it over the target. One extra create and one rename per write.

The temp name must be unique per writer, **not** the fixed `path + ".tmp"`: R1 accepts concurrent
CLI-vs-dashboard saves, and two writers sharing one temp inode (both opening it `O_TRUNC`) could
interleave their bytes before either renames, leaving a mixed file that may not parse or may parse
into a wrong table — strictly worse than R1's documented last-writer-wins.
`os.CreateTemp`'s unique name removes that at no extra cost. The rename is atomic, so a concurrent
`Loader` read sees either the whole old file or the whole new one. Change detection is unaffected:
the renamed file carries a fresh mtime/size, so `Loader` re-reads.

*Ceiling*: two processes racing a read-modify-write still lose one update. Atomicity removes
partial/empty reads; it does not remove the lost-update race.

**5. `cli.applySet` drops its duplicated checks.** It currently does its own `strings.Cut` model
handling ([prices.go:72](../../internal/cli/prices.go#L72),
[:76](../../internal/cli/prices.go#L76)) and its own `rate < 0` test
([prices.go:84](../../internal/cli/prices.go#L84)). Delete both and call the exported checks, so the
CLI and `Save` share one guard, and `--set`'s rejection message comes from that shared check.

The price CLI tests live in **`internal/cli/cli_test.go`**, not a `prices_test.go` — no such file
exists. Its four cases (`TestPricesSetWritesTheFileAndPrintsTheUnit`,
`TestPricesUnsetRevertsToUnpriced`, `TestPricesRejectsBadRateWithoutWriting`,
`TestPricesPrintsTheShippedTableWithNoFlags`) assert only `err != nil` on rejection, with no
assertion over the message text, so they keep passing unchanged and there is nothing to "move with"
the check. What this bead actually adds there is one **new** case asserting the shared check's
message for a name containing a newline (Outcome §5).

## Rationale

One guard covering every writer by construction is smaller than three copies of the check, and it is
the CLI's pre-existing injection instance that gets fixed for free. The atomicity change is the same
trade: it is one create plus one rename, and without it this ticket ships a feature that can
permanently create the very unpriced rows the other half of the ticket exists to purge.

Both are P0 because they are the only changes here that a later bead cannot compensate for. A
non-atomic `Save` under a live `Loader` is silent data corruption; a `.`-accepting `validModel` is
silent file corruption.

## Outcome Definition

- `pricing.Save` rejects a model name containing a newline, `=`, `.`, or a space, and writes
  **nothing** on rejection; the error satisfies `errors.Is(err, pricing.ErrInvalidName)`.
- A rate of NaN, ±Inf, or negative is rejected by the shared check and satisfies
  `errors.Is(err, pricing.ErrInvalidRate)`; `0` is **accepted** (a genuinely free model, distinct
  from unset).
- `Save` writes a temp file in the target's directory and renames it over the target. No `.tmp`
  file is left behind on success or on failure. A reader never observes a truncated or empty file.
- `parseTable` rejects a dotted bare model line (`deepseek.v2`).
- `cli.applySet` contains no inline rate-range check and no private model-name handling; both come
  from `internal/pricing`.
- `lens prices --set '<name with a newline>=1'` is refused with the shared check's message.

## Test Specifications

`internal/pricing` (`pricing_test.go`):

1. Rejection table for `Save`: model names containing `\n`, `=`, `.`, and a space each return
   `ErrInvalidName` (`errors.Is`), and the target file is unchanged (compare bytes before/after).
2. Rate rejections: `NaN`, `+Inf`, `-Inf`, `-1` each return `ErrInvalidRate` via the shared check;
   `0` succeeds and reads back as `0`, distinct from unset.
3. Round trip: `Save` → `Load` preserves unset fields (`null`) and a bare-model line.
4. **Atomicity, asserted through a concurrent reader — not through the directory listing.** The
   listing check is a trap: the code being replaced is a bare
   `os.WriteFile(path, …, 0o600)` ([table.go:183](../../internal/pricing/table.go#L183)), which
   creates no temp file either and does write the new table, so "no `.tmp` in the directory" and
   "the target content is the new table" are **both already true before the change**. A test made of
   those two assertions passes against the unfixed code.

   The property that actually distinguishes truncate-in-place from temp-file+rename is the one the
   plan describes at `:50-57`: a reader landing inside the write window sees an **empty** file, and
   `parseTable("")` returns an empty table with **no error**
   ([table.go:81-114](../../internal/pricing/table.go#L81-L114)), so a captured call is silently
   stored unpriced forever. So observe a reader: seed a valid table, then loop a few hundred times
   alternating the saved content while a second goroutine repeatedly calls `pricing.Load(path)`;
   assert every `Load` returns either the old table or the new one — **never an error, and never an
   empty table**. That fails against `os.WriteFile` and passes against the rename. Keep the "no
   `.tmp` left behind" check as a cheap secondary assertion (it catches the error-path leak).
5. `parseTable` rejects a dotted bare model line (`deepseek.v2`).

`internal/cli` (`cli_test.go` — the existing price tests assert only `err != nil`, so they need no
change): add one case asserting the shared check's message for `--set` with a model name containing
a newline, and that the file was not written.

## Files to Touch

- `internal/pricing/table.go` (modify — `validModel` rune set + doc comment, exported name/rate
  checks, `ErrInvalidName`/`ErrInvalidRate`, `Save` validation + temp-file atomic write,
  `parseTable` calls the exported check)
- `internal/pricing/pricing_test.go` (modify — the cases above)
- `internal/cli/prices.go` (modify — `applySet` drops its inline checks and calls the shared ones)
- `internal/cli/cli_test.go` (modify — one new rejection-message case; there is no
  `prices_test.go`, and the four existing `TestPrices*` cases assert only `err != nil`)

---

## Review Notes

**The atomicity test could not fail, and that was the bead's whole P0 claim.** Test 4 as first
written checked that no `.tmp` is left in the directory and that the target holds the new table —
both of which are *already true* of the `os.WriteFile` this bead replaces. The property that
distinguishes truncate-in-place from temp-file+rename is only observable from a **concurrent
reader**, so the test now loops a save against a `pricing.Load` spinning in another goroutine and
asserts no `Load` ever returns an empty table or an error. `parseTable("")` returning an empty table
with no error is what makes the broken version fail this test and only this test.

**`internal/cli/prices_test.go` does not exist.** The plan's Modify table names it, and this bead
copied the row. The price CLI tests live in `cli_test.go` and assert only `err != nil` on rejection —
no message assertions exist anywhere to "track the shared check". Left uncorrected, the implementer
would either create a second home for price tests or conclude a test change that is not needed.

