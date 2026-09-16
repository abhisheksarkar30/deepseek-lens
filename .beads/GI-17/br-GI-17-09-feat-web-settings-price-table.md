# Bead br-GI-17-09: the Settings tab and the editable price table

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Design decisions D13, D14, D15

- **Priority**: P1 (high)
- **Dependencies**: br-GI-17-03, br-GI-17-04
- **Blocks**: br-GI-17-10

## Description

The dashboard surface for pricing: a fifth tab, and inside it the price table, editable in place.
This is the first half of the Settings tab; br-GI-17-10 adds its second section.

**1. One new tab named "Settings", not two (D13).** Prices and retention are both "configure lens",
the tab bar is sticky and already four items wide, and the price table is the first section — so "a
tab for showing/editing the price rates" is satisfied literally, and the second section lands beside
it rather than in a fifth button.

- `internal/web/index.html`: a fifth `<button data-view="settings">` in the tab bar
  ([index.html:17-21](../../internal/web/index.html#L17-L21)) and a `<section id="view-settings"
  class="view" hidden>` holding a Pricing section and (in br-GI-17-10) a Data section.
- `internal/web/app.js`: `views` gains `"settings"`
  ([app.js:198](../../internal/web/app.js#L198)); `showView` needs no other change if it is already
  table-driven, and a load hook for the tab fires the price fetch.

**2. Render the table from `GET /api/prices`.** One row per model; four rate cells plus the
`source` badge. `null` must render as an **empty cell**, not `0` — the whole point of D3's `null`
convention is that "no rate configured" and "this model is free" are different statements, and the
dashboard is where the difference is read.

**3. Edit in place, save one row.** Clicking a cell makes it editable; saving issues
`POST /api/prices` with that model's **whole row**, exactly what was edited. Whole-row-per-model is
what the route takes (D3), so the client sends what it has rather than a diff.

**4. Re-render from the response, not from local state.** `POST /api/prices` returns the same shape
`GET` does, so the table re-renders from what was actually written. This is what makes R1's
lost-update race — a concurrent `lens prices --set` — surface as "the table now shows the value
that won" instead of as a stale UI that silently disagrees with the file.

**5. Keep the editor's state minimal (R7).** A dirty flag and one save call. `internal/web` has no
JS test runner, so editor logic is verified by a throwaway script only; less logic means less that
can be wrong and less that verification misses. Do not build an undo stack, a multi-cell batch, or a
debounced autosave — none is asked for.

**6. Accessibility carries over (D14).** A glyph never carries meaning alone — the `source` badge
follows `costBadge`'s existing pattern of a `title` plus an `aria-label`, never a bare `?`.
Interpolated text is escaped before it reaches an attribute: model names come from a file the user
edits by hand, and `D4`'s tightening does not cover every name already in the table. Anything
revealed by a click scrolls itself into view.

**7. Styles.** `internal/web/style.css` gains the editable-cell treatment and the destructive-action
treatment. The latter is written here and used by br-GI-17-10, so the two sections do not invent two
different looks for "this deletes data".

## Rationale

The dashboard is where every cost is displayed, and until now a row showing `unpriced` gave the user
no way to fix it without leaving the browser and recalling the `model.field=rate` syntax. The
mechanism already works — `lens prices` writes the file and `Loader` re-reads it live (D1) — so this
bead adds no pricing machinery at all, only a surface over the existing file.

Re-rendering from the response rather than from local state is the one design choice here worth
stating, because the cheaper version (optimistically update the DOM) is exactly the one that hides
the lost-update race.

## Outcome Definition

- A fifth tab, "Settings", appears in the tab bar and switches to `#view-settings`.
- The Pricing section renders one row per model from `GET /api/prices`, with `path` and
  `peak_multiplier` shown.
- An unset rate renders as an empty cell; a `0` rate renders as `0`. The two are visually distinct.
- The `source` badge has a `title` and an `aria-label`, never a bare glyph.
- Editing a cell and saving issues one `POST /api/prices` for that row and re-renders from the
  response body.
- A rejected save (400) leaves the table showing the server's last-known state rather than the
  rejected value.
- `GET /api/prices` answering 503 (unwired) renders a plain "pricing unavailable" state, not an
  empty table.
- `node --check internal/web/app.js` passes.

## Test Specifications

No JS test runner, so the three-step web process applies (D15). `internal/web` is `go:embed`-ed, so
every byte assertion is made against what the **running server serves**, never against the files on
disk — a binary built before the edit keeps serving the old assets. This repo has already been bitten
by that: check the served bytes before debugging dashboard code.

1. `node --check internal/web/app.js`.
2. Served-asset assertion: `curl -s http://localhost:<port>/app.js | grep -q 'view-settings'` and
   `grep -q '"settings"'`; `curl -s http://localhost:<port>/ | grep -q 'id="view-settings"'`.
3. Manual, in a browser:
   - the Settings tab loads the table; a model with no rates shows empty cells, and a model with a
     `0` rate shows `0` — the `null`-vs-`0` distinction, verified through the UI and not only in the
     API test;
   - editing a cell and saving updates the table **and** `lens prices` prints the new value — the
     end-to-end proof that the browser wrote the file the CLI reads;
   - setting a rate for an `unknown-model` row moves it out of that bucket on the Stats tab, because
     `Loader` re-read on the next stat — the integration test of D1's premise, seen through the UI;
   - a `POST` rejected with 400 (e.g. a model name with a space) leaves the table on the server's
     value and names the reason.
4. Throwaway extract-and-run script for the dirty flag: load table → edit → dirty is true → save →
   dirty is false; a rejected save leaves dirty true. Not committed, per the existing no-JS-harness
   convention.

## Files to Touch

- `internal/web/index.html` (modify — the Settings tab button, `#view-settings` with its Pricing
  section)
- `internal/web/app.js` (modify — `views` entry, `showView` load hook, price table render, in-place
  edit, save, re-render from the response)
- `internal/web/style.css` (modify — editable-cell and destructive-action styles)
