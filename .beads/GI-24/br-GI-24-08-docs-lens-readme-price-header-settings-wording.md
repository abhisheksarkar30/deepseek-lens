### Bead 8: `docs` lens: README, price-file header, and the Settings wording

- **Bead ID**: br-GI-24-08
- **Priority**: P1 (high)
- **Original Estimate**: 1h
- **Dependencies**: br-GI-24-04, br-GI-24-05
- **Blocks**: br-GI-24-09

**Description**:

The three prose surfaces that still describe a hardcoded window, corrected. **All three files are region-shared with earlier beads and the edits here must not touch those regions.**

**1. `README.md` peak-pricing prose** (`:143-154`). Today it says the window is `01:00–04:00 and 06:00–10:00 UTC, Monday through Friday` with no mention of holidays. Rewrite to state DeepSeek's actual rule: peak applies inside the window **on a working day**, and everything else — weekends *and* statutory holidays — is off-peak; the working day set is configurable via `off_peak_dates` / `work_dates`. Add the **forward-only note** (D8): fixing the calendar cannot repair rows already mispriced at 2× on a 2026 holiday — those keep their doubled `cost_usd` and their `peak_pricing` warning, because that is what the rule of the day did. State it plainly so a reader who expects their October 2026 rows to self-correct is not surprised (§8.6).

Cite the State Council notice (国办发明电〔2025〕7号, 2025-11-04) as the source of the shipped dates, and say plainly that **DeepSeek's own treatment of 调休 is unverified** — the shipped `work_dates` asserts something no DeepSeek source confirms (§8.4). This is the one place the story knowingly encodes an unverified claim; name it rather than bury it.

**Do not touch** the `peak_pricing` warning-table row at `:103` (inside the `BEGIN/END warning kinds` markers) — that is br-GI-24-04's, and `readme_test.go` ties it to `kinds.go` by construction.

**2. The price-file header comment** (`internal/pricing/table.go:236-238`). Today it reads: `# window (01:00-04:00 and 06:00-10:00 UTC, Mon-Fri) — lens detects and / # applies that multiplier itself; it is not configurable here.` Qualify it: the window is calendar-driven, so a statutory holiday (or a 调休 make-up day) can override it. The header is written into every `prices.toml` `Save` emits, so the sentence is read by every user who opens the file.

**3. The Settings-tab wording** (`internal/web/index.html:150-154` — the existing `<p class="hint">`). Today it says a peak multiplier "applies where the **upstream signals** a peak window" — false: lens computes it from the request's own timestamp, the upstream signals nothing. Correct the sentence to say lens applies the 2× itself, on the configured calendar. **Edit only the sentence text inside the existing paragraph.** The read-only calendar *display* is br-GI-24-05's new sibling element — do not add it here, and do not remove it.

**Rationale**:

R/§2.6/§6. These are the difference between a fix and a documented fix. The README's per-call warning and prose are the only surfaces a user with no plugin ever sees; the price-file header is the note above the rates they edit; the Settings sentence actively misdescribes who decides peak. Each currently names the hardcoded window the rest of the story makes configurable, so leaving any of them is a document that contradicts the code on the same screen.

**Outcome Definition**:

- `README.md`'s peak-pricing prose names holidays/weekends as off-peak, names the two config keys, carries the forward-only note, cites the notice, and states the 调休 unverified-ness.
- `README.md`'s `peak_pricing` warning-table row is unchanged by this bead.
- `internal/pricing/table.go`'s price-file header says the window is calendar-driven and can be overridden by a holiday/work day.
- `internal/web/index.html`'s Settings sentence says lens applies the 2× itself on the configured calendar; the calendar display element added by br-GI-24-05 is present and untouched.
- `go test ./internal/analyze/...` still passes (`readme_test.go` unaffected).

**Test Specifications**:

Documentation, so the verification is the searches (the br-GI-21-08 / br-GI-17-11 discipline — a search that returns nothing *before* the change is a broken search, not a clean tree):

1. **Positive control, before editing**: `rg -n -e 'Mon-Fri' -e 'Monday through Friday' -e 'upstream signals' README.md internal/pricing/table.go internal/web/index.html` — record the hits (README prose, the price header, the Settings sentence) as the control that the search finds known stale text.
2. Edit.
3. Re-run; assert the hits from the control are gone. Use separate `-e` terms, never `\|` (under ripgrep `\|` is a literal pipe and matches nothing — the trap GI-21's bead documents).
4. Assert the README's `peak_pricing` row text (inside the markers) is **byte-identical** to before this bead — this bead must not have touched it.
5. `go test ./internal/analyze/...` passes.

**Files to Touch**:
- `README.md` (modify — prose at `:143-154` only; not the table row at `:103`)
- `internal/pricing/table.go` (modify — the header comment at `:236-238`)
- `internal/web/index.html` (modify — the Settings sentence at `:150-154` only; not the calendar display)
