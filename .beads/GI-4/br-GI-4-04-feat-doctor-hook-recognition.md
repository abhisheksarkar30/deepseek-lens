# Bead br-GI-4-04: doctor reports whether the plugin hooks recognize this route

**Plan Reference**: `docs/planning/GI-4-peak-cost-and-hook-integration.md` §4.5

- **Priority**: P2 (medium) — the one bead in this story that can be dropped without weakening the
  fix. Say so explicitly rather than implementing it silently.
- **Dependencies**: br-GI-4-07
- **Blocks**: br-GI-4-06

## Description

After br-GI-4-07, the plugin hooks decide "is this session DeepSeek-backed?" with a two-clause
predicate evaluated against the session's **effective** `ANTHROPIC_BASE_URL` — the value in
`~/.claude/settings.json`:

> recognized when the effective `ANTHROPIC_BASE_URL` contains `deepseek`, **or** equals the base
> URL that `~/.claude/.deepseek-env.json` declares.

A lens-fronted route is reached by the *second* clause only — `http://127.0.0.1:8787` contains no
`deepseek` — and that clause fires only when the overlay declares the address lens serves. That is
a setup step, and getting it wrong has **no symptom**: the peak guard simply never fires, and the
user learns about it from a 2x invoice.

`lens doctor` is where this repo already puts "state that is true but invisible" — it prints the
effective config, bind addresses, and warnings, and it already reaches outside its own process
for the live-sink check (`liveStatsCheck`) and does filesystem probes (`dirWritable`,
`hostResolves`). Add one check in `runChecks`:

```
provider_hooks   pass   peak guard recognizes this route (effective ANTHROPIC_BASE_URL is the lens
                        proxy, declared by ~/.claude/.deepseek-env.json)
provider_hooks   warn   peak guard will NOT fire: ~/.claude/settings.json points ANTHROPIC_BASE_URL
                        at http://127.0.0.1:8787, but ~/.claude/.deepseek-env.json declares a
                        different address
```

The four combinations of overlay presence and effective URL resolve as:

| Overlay (`~/.claude/.deepseek-env.json`) | Effective `ANTHROPIC_BASE_URL` | Result |
|---|---|---|
| absent | any | `pass` + note — plugin not managing this machine; nothing to warn about |
| present | contains `deepseek` | `pass` — guard fires by substring |
| present | the lens address, overlay declares the **same** address | `pass` |
| present | the lens address, overlay declares a **different** address | `warn` — the genuine blind spot, detail names both |

The check reads the same two files the hooks read — `~/.claude/settings.json` for the **effective**
`ANTHROPIC_BASE_URL`, `~/.claude/.deepseek-env.json` for the declared value — and evaluates that
predicate. It is the **third copy of the predicate** (the guard and the toggle are the other two;
see the plan's §8.1), so it must be the predicate itself, written once, in the same clause order
the hooks apply it: substring on `deepseek` first, then equality with the declared URL. A check
that merely *approximates* the thing it checks — comparing the overlay's value against
`cfg.ProxyAddr` instead — reintroduces exactly the drift this story exists to close.

**Two values are compared, and they are not the same shape.** The effective URL and the overlay's
declared value are both full URLs (`http://127.0.0.1:8787`), so they compare directly; but
`cfg.ProxyAddr` is a bare `host:port` (`127.0.0.1:8787` — `internal/config/config.go:62`), so when
deciding whether the effective URL is *the address lens serves* (the warn branch), compare it
against `"http://" + cfg.ProxyAddr` (scheme-normalized), never the raw `ProxyAddr`.

**Resolve the home directory the way the rest of the codebase does.** `providerHookCheck` must
read `os.Getenv("HOME")` **before** falling back to `os.UserHomeDir()`, the same precedence as
`config.userHomeDir` (`internal/config/config.go:83`) and `pricing`'s copy
(`internal/pricing/table.go:21`). On Windows — this repo's platform — `os.UserHomeDir()` returns
`USERPROFILE` and ignores `HOME`, so a check that called it directly would read the developer's
real `~/.claude/` (the exact leak the "throwaway `HOME`" test forbids) and would pass or fail on
machine state rather than on the fixture. Reuse the existing helper: promote `config.userHomeDir`
to an exported `config.UserHomeDir()` (updating its two internal callers in `config.go`) and call
it from `cli`, rather than adding a third inline copy. Why reuse beats a local copy here: `pricing`
duplicates the helper precisely so `pricing` stays a leaf package that never imports `config`
(`internal/pricing/table.go:19` — "rather than exported out of internal/config so **this** stays a
leaf package", where "this" is `pricing`) — but that reasoning does not transfer to `cli`, which
**already** imports `internal/config` (`runChecks(cfg *config.Config)` in `doctor.go`), and `doctor.go`
resolves no home directory today (`grep` over `internal/cli` for `UserHomeDir` / `os.Getenv("HOME")` /
`userHomeDir` / `.claude` returns no matches). So an inline copy in `doctor.go` would be the *third*
copy **and** the only one with no leaf-package justification; exporting `config.UserHomeDir` grows
`config`'s API by one small resolver and prevents the unjustified copy.

Behaviour that matters:

- **WARN, never FAIL.** A user who does not use the plugin hooks at all, or who deliberately runs
  direct, must still get a successful `doctor` — the same reasoning `liveStatsCheck` already
  documents for a missing `serve`. Reporting a missing integration is not a reason to fail the
  command.
- **Absent files are not an error, and a missing overlay is never a `warn`.** A missing
  `~/.claude/.deepseek-env.json` is the plugin-presence signal read as absent — the plugin is not
  managing this machine, so there is no guard to be broken and nothing to warn about (Rationale).
  It short-circuits to `pass` with a note, whatever the effective URL. A missing `settings.json`,
  or an `env` block with no `ANTHROPIC_BASE_URL`, likewise gives nothing to evaluate: a pass-through
  note, exit 0. No missing file makes `doctor` exit non-zero.
- **Accept both overlay shapes**, the legacy flat one and the `env`/`settings` one — the same two
  shapes br-GI-4-07's JS predicate handles.

The declared URL is extracted with a small targeted read, not a JSON parser, for the same reason
the guard does it (the file is small, hand-written, and holds one key of interest). Keeping that
extraction as a small helper here, next to the check, keeps the divergence between this and the
bash copy visible in one place.

## Rationale

Every other bead in this story either fixes a silent failure or creates a new one. This is the
only one that makes the integration *verifiable* by the person relying on it, and it is the
answer to "how would I know it stopped working?" — the question the hooks' silence otherwise
leaves unanswerable.

**The check is the predicate's third copy, so it must be the predicate — not a proxy for it.** The
guard and the toggle already carry the rule, and the plan's §8.1 names drift between the copies as
the top risk. A doctor check that answered a *nearby* question — "does the overlay's URL equal
`cfg.ProxyAddr`?" — would disagree with the hooks the moment they disagree with each other, and
would print `warn` for every ordinary direct-DeepSeek user whose overlay names `api.deepseek.com`
(the guard fires on that route by the substring clause). Writing the predicate itself, in the
hooks' clause order, is what keeps this copy from becoming the drift it exists to surface.

It is P2 because it is diagnostic only: dropping it costs the user a way to check, not
correctness.

**The overlay doubles as the plugin-presence signal:** its presence is the best available proxy for
"this machine is managed by the plugin," so a missing overlay means there is no guard to be broken
and a `warn` would be a false alarm of exactly the class F2.1 exists to prevent — hence absence
means `pass`, not `warn`.

## Outcome Definition

- `go test ./internal/cli/...` passes.
- `lens doctor` prints a `provider_hooks` row.
- The four combinations of overlay presence and effective URL resolve as:

| Overlay (`~/.claude/.deepseek-env.json`) | Effective `ANTHROPIC_BASE_URL` | Result |
|---|---|---|
| absent | any | `pass` + note — plugin not managing this machine; nothing to warn about |
| present | contains `deepseek` | `pass` — guard fires by substring |
| present | the lens address, overlay declares the **same** address | `pass` |
| present | the lens address, overlay declares a **different** address | `warn` — the genuine blind spot, detail names both |

- No `settings.json`, or no effective `ANTHROPIC_BASE_URL` to read → status `pass` with a note, and
  `doctor` exits 0.
- The check never causes `doctor` to exit non-zero.

## Test Specifications

- Unit Tests (`internal/cli/cli_test.go`, following the existing doctor-check test pattern). The
  four predicate combinations must each be covered, exercising **both** clauses:
  - **Effective URL contains `deepseek`, overlay declares the direct DeepSeek URL** → `pass`
    (substring clause). This is the case that regresses today: a direct-DeepSeek user whose overlay
    names `https://api.deepseek.com/anthropic` is recognized by the guard, yet the old
    overlay-vs-`ProxyAddr` check warned at them.
  - **Effective URL contains `deepseek`, overlay declares a different URL** → `pass` (substring
    clause; still recognized).
  - **Effective URL is the address lens serves, overlay declares the same URL** → `pass` (equality
    clause).
  - **Effective URL is the address lens serves, overlay absent** → `pass` with a note (a missing
    overlay short-circuits to `pass`, whatever the effective URL — the corrected behaviour; the
    round-2 apply wrongly warned here).
  - **Effective URL is the address lens serves, overlay declares a different URL** → `warn`; the
    detail names both. Use `http://127.0.0.1:8787` for the effective URL with `cfg.ProxyAddr`
    `127.0.0.1:8787`, so this case also exercises the scheme normalization.
  - **No `settings.json`** (no effective URL to read) → `pass`, exit 0.
  - **Malformed overlay JSON** → `pass` (not a crash, not a warn): an unreadable overlay is a
    plugin problem, and this check must not turn it into a doctor failure.
  - **Sectioned overlay shape** → `pass`: the same URL found under `env` rather than at the top
    level.
  - Point the check at a throwaway `HOME` (`t.Setenv("HOME", tmp)`) so the developer's real
    `~/.claude/` is never read. This only isolates on Windows if the check resolves `HOME` before
    `os.UserHomeDir()` — see the Description.
- Integration Tests: none — the check is a pure read of two files.
- E2E: deferred to `/develop-tests`.

## Files to Touch

- `internal/cli/doctor.go` (modify — `providerHookCheck` and its registration in `runChecks`; the
  URL comparison normalizes the scheme and the home dir resolves via `config.UserHomeDir()`)
- `internal/cli/cli_test.go` (modify — the eight cases above, against a throwaway `HOME`)
- `internal/config/config.go` (modify — export `userHomeDir` as `UserHomeDir`, update its two
  internal callers; no behaviour change)
