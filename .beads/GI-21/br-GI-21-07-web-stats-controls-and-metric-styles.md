### Bead 7: `style.css` — the stats-controls rule and the `.metric` / `.metric.active` rule

- **Bead ID**: br-GI-21-07
- **Priority**: P2 (low — styling only; without it the native controls and the metric toggle still function, just unstyled against the dark theme)
- **Original Estimate**: 0.5h
- **Dependencies**: br-GI-21-03, br-GI-21-05 (the class names this rule styles are established by the markup)
- **Blocks**: None

**Description**:

Two new rules in `internal/web/style.css`. The stylesheet has **no generic `<input>` or `<select>`
rule**, so an unstyled native date input and select render with browser-default chrome against the
dark theme; and `.pager select` (`style.css:110-116`) is a CSS *descendant* selector scoped inside a
`<div class="pager">` wrapper, so it cannot be reused for the standalone stats controls (D4).

1. **The stats controls rule (D4).** Style the granularity `<select>` and the two
   `<input type="date">` fields with the neutral-border language the pager's navigational controls
   already use:

   ```css
   .stats-controls {
     display: flex;
     align-items: center;
     gap: 10px;
     margin-bottom: 8px;
     font-size: 0.85rem;
   }
   .stats-controls select,
   .stats-controls input[type="date"] {
     background: var(--bg-alt);
     color: var(--fg);
     border: 1px solid var(--border);
     border-radius: 6px;
     padding: 4px 6px;
   }
   ```

   **`var(--border)`, not `var(--accent)` (F2.3).** `td.price-cell input` (`style.css:259-269`) uses
   `var(--accent)` for its border — the visual language for "directly editable data". The stats
   controls are navigational filters, like the pager's `<select>`, which uses `var(--border)`
   (`style.css:110-116`); using `var(--accent)` here would signal "editable data" for a control that
   only filters.

2. **The metric toggle rule (D5, F6.2).** Match the visual appearance of `.tab` / `.tab.active`
   (`style.css:73-82`) via a **distinct** class:

   ```css
   .metric {
     background: var(--bg-alt);
     color: var(--fg);
     border: 1px solid var(--border);
     border-radius: 6px;
     padding: 6px 10px;
     cursor: pointer;
     font-size: 0.85rem;
   }
   .metric.active {
     background: var(--accent);
     color: var(--accent-fg);
     border-color: var(--accent);
   }
   ```

   The look is shared by **copying the declarations**, not by sharing the `.tab` selector. A shared
   `.tab` class would put the metric buttons inside `showView()`'s unscoped
   `document.querySelectorAll(".tab")` sweep (`app.js:224-226`), which toggles `active` off every
   `.tab` element whose `data-view` does not match the shown view — stripping the selected metric on
   every view transition (D5, F6.2). The new selector is what keeps the metric buttons outside that
   sweep while still looking identical.

Place both rules near the existing `svg#stats-chart` block (`style.css:285-295`) so the Stats-tab
styles stay together.

**Rationale**:

D4 — the controls need a neutral-border rule the stylesheet does not otherwise have, matching the
pager's navigational filter look rather than the price-cell's editable-data look. D5/F6.2 — the metric
toggle must look like the tab bar without joining the `.tab` selector whose unscoped sweep would
deselect it; the `.metric` class is that separation, and this rule is its styling half.

**Outcome Definition**:

- The granularity `<select>` and both date inputs render with `var(--bg-alt)` background,
  `var(--fg)` text, and a `var(--border)` border in both light and dark themes.
- The `.metric` buttons render like `.tab` buttons, and the `.metric.active` button renders like
  `.tab.active` (accent background, accent foreground).
- No existing selector is modified; the two rules are additive only.
- `curl -s http://localhost:<port>/style.css` contains both `.stats-controls` and `.metric.active`.

**Test Specifications**:

- Integration Tests (served-asset assertions — `internal/web` is `go:embed`-ed, so assert the bytes
  the running server serves, never the file on disk; same technique GI-17 used):
  - `curl -s http://localhost:<port>/style.css | grep -q '\.stats-controls'`
  - `curl -s http://localhost:<port>/style.css | grep -q '\.metric\.active'`
  - positive control for the negative: confirm the pre-edit `style.css` served **no** `.metric` rule,
    so the post-edit hit is evidence the rule landed rather than that the grep matched something else.
- Unit Tests: none — CSS only.
- E2E: manual — in both light and dark theme, open the Stats tab and confirm the select/date inputs are
  legible (not browser-default white-on-dark chrome) and the selected metric button is visibly
  highlighted.

**Files to Touch**:
- `internal/web/style.css` (modify — the `.stats-controls` rule and the `.metric` / `.metric.active` rule)
