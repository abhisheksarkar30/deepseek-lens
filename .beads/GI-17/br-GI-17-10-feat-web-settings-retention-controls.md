# Bead br-GI-17-10: the Data section — retention state and the two purge controls

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Design decisions D5, D11, D12, D14, §Risk areas R2, R3, R5

- **Priority**: P1 (high)
- **Dependencies**: br-GI-17-07, br-GI-17-09
- **Blocks**: none

## Description

The second section of the Settings tab: what retention is configured to, and the two one-off purges.
This is the only place in the product where a user can irreversibly delete captured data with a
click, so the copy and the confirmation are the feature.

**1. The tab displays the configured value; it does not set it (D12).** Rewriting `config.toml`
would have to reconstruct a hand-editable document whose comments and unrecognized keys
`parseFlatFile` drops on the way in, and that file also holds `ProxyAddr` and `DBPath`. Corrupting
it is a bad trade for a value that already has three inputs. So the section **shows** the resolved
`days` and points at the flag/env/file for changing it, and offers the outcome the user actually
wants — purge now — without the destructive write.

**2. Two states, driven by `GET /api/retention`.**

- **`days <= 0` ("keep forever"):** show that state plainly, and **offer no older-than purge
  action**. `cutoff` is `null` in this state, so there is no threshold to name and nothing eligible.
  The button must not exist rather than exist-and-do-nothing.
- **`days > 0`:** show the configured days, the `cutoff`, and the eligible preview —
  `eligible_requests`, `eligible_bytes`, `oldest`, `newest`.

**3. The older-than action is preview-then-confirm.** The numbers on the confirm step are the
preview's, not an estimate. `oldest`/`newest` are `null` exactly when `eligible_requests` is `0` —
a state that is reachable with `days > 0` (a threshold older than the data) and must render as
"nothing to purge" rather than as a broken date.

**4. The unpriced action is separate, explicitly named, and available when retention is off.**
`unpriced_requests`/`unpriced_bytes` do not depend on `days`, so the unpriced action stays available
under "keep forever" — it is the answer to "my all-time total is understated", which has nothing to
do with retention.

**Its confirm copy must label the number as the positive-token unpriced count**, not as "unpriced
records". `unpriced_requests` is a strict **subset** of the Stats tab's `unpriced` group — the D5
predicate excludes zero-token rows, which `StatsByCostSource` counts — so the two on-screen figures
**differ by design**. A user comparing this confirm to the Stats tab's larger `unpriced N` must read
a label explaining the difference, not a contradiction. Getting this wrong is a support ticket
disguised as a copy decision; say what the number is.

**5. After a purge, reload the active view explicitly (R5).** The `POST /api/purge` response carries
what was actually deleted; the tab reloads the current view rather than inventing a new SSE event
type for it. The feed, the Stats tab and the sessions list all describe rows that may no longer
exist, and the cheapest correct thing is to re-ask for what is on screen.

**6. Never offer `VACUUM` here (R3).** `VACUUM` needs free space on the order of the DB size and
takes an exclusive lock that blocks ingest for seconds — a multi-second freeze with no feedback is a
bad dashboard experience. It is a CLI-only opt-in, `lens purge --vacuum`. The section may *say* the
file does not shrink after a delete and point at that command; it must not run it.

**7. Accessibility carries over (D14).** A destructive control is not a bare glyph; anything
revealed by the confirm click scrolls itself into view; interpolated values are escaped before they
reach an attribute.

## Rationale

Every other surface in this ticket can be wrong and be corrected. This one deletes the user's
capture file irreversibly, which is why it has a preview step, a labelled confirm, an
explicitly-named separate action, and no vacuum button. The `days <= 0` state is the one that most
needs to be rendered honestly: "retention is off" and "everything is eligible" are the two readings
of an unconfigured threshold, and only one of them is true.

## Outcome Definition

- The Data section shows the configured `retention_days`; it offers no control that writes
  `config.toml`.
- With `days <= 0`: the "keep forever" state is shown and **no** older-than purge action is present.
- With `days > 0`: the cutoff and the eligible preview (count, bytes, oldest, newest) are shown.
- `oldest`/`newest` null renders as "nothing to purge" rather than as an invalid date.
- The unpriced action is present and works with retention off.
- The unpriced confirm's copy names the figure as the positive-token unpriced count, and states that
  it is a subset of the Stats tab's `unpriced` group.
- Confirming a purge issues one `POST /api/purge` and then reloads the active view.
- No `VACUUM` is offered or run from the dashboard.
- `node --check internal/web/app.js` passes.

## Test Specifications

Same three-step web process as br-GI-17-09 (D15) — every byte assertion is against the **served**
assets, never the files on disk.

1. `node --check internal/web/app.js`.
2. Served-asset assertion: `/app.js` contains the retention fetch and the two action names;
   `/` contains the Data section markup.
3. Manual, in a browser, with `retention_days` set and a store seeded past the cutoff:
   - the preview's numbers match what a purge then deletes — capture the confirm's count, confirm,
     and compare against the row count that actually disappeared (the same assertion as the API
     test, made through the UI where the user reads it);
   - **the `days <= 0` state**: with retention off, the section shows "keep forever" and there is no
     older-than action to click — the case that fails if the action is merely disabled rather than
     absent;
   - the unpriced action is present with retention off, and its confirm's copy **says** the number
     is the positive-token count; compare the two on-screen figures with the Stats tab open — they
     differ, and the label is what makes that read as intended;
   - after a purge the active view reloads and the deleted rows are gone from the feed and the
     Stats totals;
   - no control anywhere in the dashboard runs `VACUUM`.
4. Throwaway extract-and-run script for the confirm-gating logic: preview → confirm enabled →
   confirm → the action fires once (not twice), and a failed `POST` surfaces an error rather than
   silently reloading.

## Files to Touch

- `internal/web/index.html` (modify — the Data section: retention state, the two actions, the
  confirm step)
- `internal/web/app.js` (modify — the retention fetch, the two-state render, the confirm gating,
  the purge call, the active-view reload)
- `internal/web/style.css` (modify — any Data-section styles not already added by br-GI-17-09)

---

## Review Notes

**No defects found in this bead**, and that is worth recording rather than leaving silent — it is the
only bead in the set that came through review unchanged. Two of its specs are load-bearing and should
not be softened during implementation:

- **The `days <= 0` state must render the older-than action *absent*, not disabled.** "Retention is
  off" and "everything is eligible" are the two readings of an unconfigured threshold, and a greyed
  button still reads as "there is something to purge here".
- **The unpriced confirm's copy must name its number as the positive-token count.** `StatsByCostSource`
  has no token condition, so the Stats tab's `unpriced N` is *larger* than what this action deletes.
  Without the label the two on-screen figures read as a contradiction, and the user is left deciding
  which number to believe about an irreversible delete.

**VACUUM stays out of the dashboard** (R3): a multi-second ingest-blocking freeze with no feedback is
the wrong shape for a browser action, and `lens purge --vacuum` is where it belongs.

