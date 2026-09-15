# Bead br-GI-9-01: warning badges name themselves, and the session drill-down scrolls into view

- **Priority**: P1
- **Dependencies**: none
- **Blocks**: none

## Description

Two defects with one cause: the `⚠` glyph was written as decoration rather than as the control it
is meant to be.

**The badge had no accessible name.** `internal/web/app.js` built it inline at four sites as a bare
`<span class="badge warn">⚠ N</span>`, with no `title` and no `aria-label`. `costBadge` in the same
file already demonstrates the rule this repo holds elsewhere — a glyph is never the only carrier of
meaning — so this bead routes every site through one `warnBadge(count, hint)` helper that mirrors
it. The three `<th>⚠</th>` column headers are the same nameless glyph and get titles too.

**The drill-down opened off-screen.** `openSession` unhid `#session-detail` but never scrolled to
it, and that panel sits after the sessions table, which is routinely taller than the viewport. So
the fetch succeeded, the panel rendered correctly, and the click still read as dead on every row.
`scrollIntoView` fixes the click; `scroll-margin-top` on `.detail-panel` keeps the sticky header
from covering the panel's own title once it is scrolled to the top.

## Outcome Definition

- Every `⚠` in the dashboard carries both a `title` and an `aria-label` naming what it counts.
- Hovering a warning badge shows that text; hovering a `⚠` header names the column.
- Clicking any session row brings `#session-detail` into view with its title clear of the sticky
  header.

Verification is manual rather than automated: `internal/web` has no JS test harness and this bead
does not add one (CLAUDE.md's no-build-toolchain rationale for the dashboard). The check is the
served asset — confirm the running server returns the new markup — plus the eyeball on the two
behaviours.

## Files to Touch

- `internal/web/app.js` (modify — `warnBadge` helper, its four call sites, `scrollIntoView`)
- `internal/web/index.html` (modify — `title` on the three `⚠` column headers)
- `internal/web/style.css` (modify — `scroll-margin-top` on `.detail-panel`)
