# Bead br-GI-4-04: doctor reports whether the plugin hooks recognize this route

**Plan Reference**: `docs/planning/GI-4-peak-cost-and-hook-integration.md` §4.5

- **Priority**: P2 (medium) — the one bead in this story that can be dropped without weakening the
  fix. Say so explicitly rather than implementing it silently.
- **Dependencies**: br-GI-4-07
- **Blocks**: br-GI-4-06

## Description

After br-GI-4-07, the plugin hooks decide "is this session DeepSeek-backed?" with a two-clause
predicate evaluated against the session's **effective** `ANTHROPIC_BASE_URL` — the value in Claude
Code's `settings.json`:

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

| Overlay (`.deepseek-env.json`, in Claude Code's config dir) | Effective `ANTHROPIC_BASE_URL` | Result |
|---|---|---|
| absent | any | `pass` + note — plugin not managing this machine; nothing to warn about |
| present | contains `deepseek` | `pass` — guard fires by substring |
| present | the lens address, overlay declares the **same** address | `pass` |
| present | the lens address, overlay declares a **different** address | `warn` — the genuine blind spot, detail names both |

The check reads the same two files the hooks read — `settings.json` for the **effective**
`ANTHROPIC_BASE_URL`, `.deepseek-env.json` for the declared value, both under Claude Code's config
directory, which the next section resolves — and evaluates that predicate. It is the **third copy
of the predicate** (the guard and the toggle are the other two;
see the plan's §8.1), so it must be the predicate itself, written once, in the same clause order
the hooks apply it: substring on `deepseek` first, then equality with the declared URL. A check
that merely *approximates* the thing it checks — comparing the overlay's value against
`cfg.ProxyAddr` instead — reintroduces exactly the drift this story exists to close.

**Two values are compared, and they are not the same shape.** The effective URL and the overlay's
declared value are both full URLs (`http://127.0.0.1:8787`), so they compare directly; but
`cfg.ProxyAddr` is a bare `host:port` (`127.0.0.1:8787` — `internal/config/config.go:62`), so when
deciding whether the effective URL is *the address lens serves* (the warn branch), compare it
against `"http://" + cfg.ProxyAddr` (scheme-normalized), never the raw `ProxyAddr`.

**Resolve Claude Code's config directory by Claude Code's rules, not lens's.** This is a
*different question* from "where is the user's home", and the two have different correct answers.

Claude Code stores its files under a directory that `CLAUDE_CONFIG_DIR` relocates wholesale —
settings, session history and plugins together — and its documented Windows rule is that
`~/.claude` means `%USERPROFILE%\.claude`, **not** `$HOME/.claude`. `config.userHomeDir`
(`internal/config/config.go:83`) resolves `$HOME` **first**, deliberately, so that tests and users
can redirect *lens's own* files (`config.toml`, `prices.toml`, the database) uniformly across
platforms. That precedence is right for those files and wrong here: on Windows, whenever `$HOME`
differs from `%USERPROFILE%` — the ordinary state of a Git Bash session, on this repo's target
platform — it resolves a directory Claude Code never reads, and the check would then answer
confidently about a file the hooks never touch.

So `providerHookCheck` resolves through a small `claudeConfigDir()` beside it, in this precedence:

1. `CLAUDE_CONFIG_DIR`, if set — Claude Code's own relocation variable, and the highest-priority
   answer when a user has deliberately moved their config.
2. `%USERPROFILE%\.claude`, if `USERPROFILE` is set — the documented Windows rule, and the first
   thing Node's `os.homedir()` resolves in the hooks.
3. `$HOME/.claude`, if `HOME` is set.
4. `os.UserHomeDir()` + `/.claude` — last resort.

Steps 2 to 4 are exactly what Node's `os.homedir()` resolves, so the check finally reads the same
file the hooks read. `config.UserHomeDir` is therefore **not** exported and
`internal/config/config.go` is **not** touched by this bead. The `pricing` leaf-package reasoning
(`internal/pricing/table.go:19` — "rather than exported out of internal/config so **this** stays a
leaf package") is not what decides this; the deciding fact is that a helper built to relocate
lens's own files is the wrong tool for locating Claude Code's.

Keep it small, unexported, and next to its only caller — it has one call site and no reason to be
an API.

Behaviour that matters:

- **The check is incapable of returning `FAIL`.** `runDoctor` returns a non-zero exit the moment
  any check FAILs (`internal/cli/doctor.go:41-45`), so a `FAIL` from `provider_hooks` would break
  `doctor` for a user with no plugin — the one way this bead, which exists to make the integration
  *visible*, could make lens *fail* because of it. `WARN` is the ceiling, reserved for the single
  genuinely actionable state. A user who does not use the plugin at all, or who deliberately runs
  direct, must still get a successful `doctor` — the same reasoning `liveStatsCheck` already
  documents for a missing `serve`.
- **Every no-plugin condition resolves to `pass` with a note, whatever the effective URL.** These
  are the states a user with no plugin — or no Claude Code, or a Cline-only setup — actually
  presents, and each is a case the tests must pin:
  - no config directory at all (the client is not Claude Code, or the directory was never created);
  - a config directory with no `settings.json` in it;
  - `settings.json` present but unreadable (permissions, a lock, an ACL);
  - `settings.json` present but not a regular file (a directory, a broken symlink);
  - `settings.json` with no `env` block, or an `env` block with no `ANTHROPIC_BASE_URL`;
  - an overlay that is absent, unreadable, or not valid JSON.

  A missing overlay is also the plugin-**presence** signal read as absent: the plugin is not
  managing this machine, so there is no guard to be broken and nothing to warn about (Rationale).
  It short-circuits to `pass` whatever the effective URL. Nothing in this list makes `doctor` exit
  non-zero, and none may surface a raw file error to the user.
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

**It is also the bead that carries the no-plugin guarantee, because it is the only bead that can
break it.** Every other lens bead in this story touches nothing the plugin owns, so lens works
without a plugin for free. This one reads a foreign file, and `runDoctor` turns a single `FAIL`
into a non-zero exit — so for a user with no plugin, a careless implementation of a check *about*
the plugin is the one way this story could break `doctor`. That is why the Description enumerates
the no-plugin conditions exhaustively and the tests pin each one, rather than leaving them to be
discovered by the first user who has no Claude Code installed.

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

| Overlay (`.deepseek-env.json`, in Claude Code's config dir) | Effective `ANTHROPIC_BASE_URL` | Result |
|---|---|---|
| absent | any | `pass` + note — plugin not managing this machine; nothing to warn about |
| present | contains `deepseek` | `pass` — guard fires by substring |
| present | the lens address, overlay declares the **same** address | `pass` |
| present | the lens address, overlay declares a **different** address | `warn` — the genuine blind spot, detail names both |

- No `settings.json`, or no effective `ANTHROPIC_BASE_URL` to read → status `pass` with a note, and
  `doctor` exits 0.
- With **no config directory at all** — no Claude Code on the machine, or a client that never had
  one — `provider_hooks` reads `pass` with a note and `doctor` exits 0.
- The check never causes `doctor` to exit non-zero, under any of the conditions enumerated in the
  Description.

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
- **The no-plugin cases** — one test each, all asserting `pass` **and** that the enclosing
  `runDoctor` returns a nil error, since a `pass` that still produced a non-zero exit would defeat
  the point:
  - **No config directory at all**: point `CLAUDE_CONFIG_DIR` at a path that does not exist. This
    is the Cline-only user and the plain `curl` user, and it is the case that must not become a
    file-read error.
  - **Config directory present, no `settings.json`** → `pass`, exit 0.
  - **`settings.json` unreadable** → `pass`: the check swallows the permission error rather than
    propagating it.
  - **`settings.json` is a directory** → `pass`: the same discipline for a non-regular file.
  - **`settings.json` with no `env` block**, and **an `env` block holding no
    `ANTHROPIC_BASE_URL`** → `pass` (two cases: nothing to evaluate).
  - **Malformed overlay JSON** → `pass` (not a crash, not a warn): an unreadable overlay is a
    plugin problem, and this check must not turn it into a doctor failure.
  - **Unreadable overlay** → `pass`: the same, for the read-error path rather than the parse path.
  - **Sectioned overlay shape** → `pass`: the same URL found under `env` rather than at the top
    level.
- **Isolate through `CLAUDE_CONFIG_DIR`, not `HOME`.** `t.Setenv("CLAUDE_CONFIG_DIR", tmp)` points
  the check at a throwaway directory on every platform in one line, and it outranks every other
  step in the resolver. A test that set only `HOME` would be a real leak on Windows: the resolver's
  second step reads `USERPROFILE`, which is set for real on the developer's machine, so the check
  would read the developer's actual `~/.claude/` and pass or fail on machine state rather than on
  the fixture.
- **Resolver precedence** — one case per step, with the higher steps unset to reach the lower ones:
  `CLAUDE_CONFIG_DIR` wins when set; `USERPROFILE` is used when it is not; `HOME` is used when both
  are unset. These are what pin the Windows rule the Description argues for — without them the
  precedence is prose, and the divergence that motivated it can silently return.
- Integration Tests: none — the check is a pure read of two files.
- E2E: deferred to `/develop-tests`.

## Files to Touch

- `internal/cli/doctor.go` (modify — `providerHookCheck`, the `claudeConfigDir()` resolver it reads
  through, and the registration in `runChecks`; the URL comparison normalizes the scheme)
- `internal/cli/cli_test.go` (modify — the cases above, isolated via a throwaway
  `CLAUDE_CONFIG_DIR`)

`internal/config/config.go` is **not** touched. An earlier draft asked for `userHomeDir` to be
exported as `config.UserHomeDir` and called from here; once the config directory is resolved by
Claude Code's precedence rather than lens's (Description), that export has no caller and the local
resolver is the smaller, more correct change.
