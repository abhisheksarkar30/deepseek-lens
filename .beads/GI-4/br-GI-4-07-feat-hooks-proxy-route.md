# Bead br-GI-4-07: Plugin hooks recognize a proxy-fronted DeepSeek route

**Plan Reference**: `docs/planning/GI-4-peak-cost-and-hook-integration.md` §4.1

**Repo**: `D:\github\agentic-ai-artifacts` — the code for this bead lands in the **other**
repository. This bead file lives here as the story's audit trail; the commit goes into the plugin
repo's history, which uses plain conventional-commit subjects rather than this repo's `GI#<n>`
prefix.

- **Priority**: P0 (critical)
- **Dependencies**: none
- **Blocks**: br-GI-4-04, br-GI-4-08

## Description

Both hooks in the plugin decide "is this session DeepSeek-backed?" by asking whether
`ANTHROPIC_BASE_URL` contains the substring `deepseek`:

- `hooks/deepseek-peak-guard.sh:14-17`
- `hooks/deepseek-auto-toggle.js:72-76`

Route Claude Code through lens and that value becomes `http://127.0.0.1:8787`, which matches
nothing. Both hooks then fail — in opposite and equally wrong directions:

- **Peak guard** takes its `*) exit 0` branch and **never blocks**. The 2x billing event the hook
  exists to prevent proceeds unguarded, silently.
- **Auto-toggle, off-peak** computes `current = "pro"`, concludes the overlay is not applied, and
  installs the overlay over `settings.env` — **overwriting `ANTHROPIC_BASE_URL` and, where the
  overlay still declares the direct DeepSeek URL, dropping lens out of the path on the next
  launch**. The user loses observability with no message saying so. (When the overlay declares the
  lens URL instead, the same code merges the identical URL back — a needless rewrite, not a
  dropped route; see the Rationale.)
- **Auto-toggle, during peak** computes `current = "pro" == desired` and returns early, so a
  lens-fronted session keeps billing DeepSeek at 2x through the window the toggle exists to close.

The substring test was always a *proxy* for the real question. Fix the predicate to the question —
the same **rule**, implemented per language:

> A session is DeepSeek-backed when its effective `ANTHROPIC_BASE_URL` either names DeepSeek, or
> is exactly the base URL that `~/.claude/.deepseek-env.json` declares.

The second clause is the load-bearing one. That overlay file is already the toggle's declaration
of what it owns, and it *is* the machine's statement of what the DeepSeek route looks like: a
direct setup points it at `api.deepseek.com`, a lens-fronted setup points it at the lens proxy.
Keeping the substring test as the first clause means a machine with a hand-set DeepSeek URL and no
overlay behaves exactly as it does today.

Deliberately **not** chosen: treating any loopback URL as DeepSeek-backed. That guesses, and it
guesses wrong on the first machine that puts some other local proxy in front of the Anthropic API
— and the peak guard's failure mode for a wrong guess is refusing the user's work.

### `deepseek-auto-toggle.js`

The script already parses the overlay into `ownedEnv`, normalized across both the legacy flat
shape and the `env`/`settings` shape (`:87-100`). The change is:

1. Read the overlay **before** computing `current` (it is currently read after; the parse is
   needed either way).
2. Replace the `current` expression with the shared predicate, reading
   `ownedEnv?.ANTHROPIC_BASE_URL` as the declared value. When the overlay is missing or invalid
   (`ownedEnv === null`), the declared value is absent and the predicate falls back to the
   substring test — today's behaviour exactly.

**Resolve the declared value's placeholders — for the predicate only.** The overlay read at
`:87-100` is a parse only; `${VAR}` substitution happens later, at `:105-122`, and the *resolved*
value is what lands in `settings.json`. Comparing the resolved `settings.env` value against the
unresolved overlay value (`${PROBE_BASE_URL}`) can never match, so a `${VAR}` base URL — a
supported, tested shape (`test-deepseek-auto-toggle.sh:206-213`; plugin `README`) — would leave
`current` stuck and the predicate permanently inert (today's bug, plus a pointless rewrite every
off-peak launch). Resolve the overlay's `ANTHROPIC_BASE_URL` locally for the comparison. **Do not
hoist the substitution block above the early return at `:79`:** that block prints the missing-var
message and returns, so hoisting it would emit the message on *every* session start, including
already-correct ones. Only the base URL is resolved for the predicate; the substitution, its
message, and its ordering stay exactly where they are.

### `deepseek-peak-guard.sh`

This file has no JSON parser and must not grow one. It needs a single string out of a small
hand-written file, so a targeted `grep -o` on the one `ANTHROPIC_BASE_URL` key is enough — and it
is shape-agnostic, because the key is spelled the same in both overlay shapes the JS hook
supports. This matches the repo's existing taste for a tiny hand-rolled reader over a dependency
(`internal/config`'s `parseFlatFile` carries the same justification).

**Known gap — the guard does not resolve `${VAR}`.** The JS predicate resolves `${VAR}` before
comparing (see its subsection), but this guard greps the *raw* key, so an overlay declaring
`ANTHROPIC_BASE_URL: "${LENS_URL}"` is **recognized by the toggle and silently missed by the
guard**: the guard compares the literal `${LENS_URL}` against the resolved `$ANTHROPIC_BASE_URL`
and takes its `*) exit 0` branch. The two files implement the same *rule*, not the same code —
placeholder handling is the one place they currently diverge. This is left as a known limit, not
fixed here: its failure direction is "not recognized" (today's behaviour, never a regression), and
the upgrade path is to resolve the placeholder in bash too, or to make the guard delegate to a
shared resolver the JS also calls (which would also retire the shape-drift risk in §8.4).

Its failure direction is "not recognized", i.e. today's behaviour, so a miss is never a regression
— see the plan's risk §8.4.

### Both files

Each gains a comment naming the other, because **drift between these two copies is the bug being
fixed**. That coupling is the top risk in the plan (§8.1) and the comment is the cheapest thing
that makes the next editor aware of it.

## Rationale

This is the bug fix; everything else in the story is the enhancement that makes the fix worth
having. It is independent of every lens bead and can land first.

It is also the bead that decides whether the integration works at all. Without it, a lens-fronted
user has no peak protection: the guard takes its `exit 0` branch and never blocks. In the
configuration this bead fixes — the overlay declaring the lens URL — today's off-peak symptom is a
**needless rewrite** of `settings.json` (the toggle merges the same lens URL back over
`settings.env`, `deepseek-auto-toggle.js:136`, clobbering any hand-rotated key), *not* a dropped
route. The "lens is dropped" outcome belongs to the **inverse** configuration — the overlay
declaring the *direct* DeepSeek URL while `settings.json` points at lens — and this bead does
**not** repair that one; it is a setup change bead 08 documents (plan §1, §8.2).

## Outcome Definition

- `bash hooks/test-deepseek-auto-toggle.sh` passes, including the new lens-URL cases.
- With the overlay declaring a lens URL and the clock off-peak, running the toggle leaves
  `settings.json` **byte-identical** — the lens base URL is not overwritten.
- With the overlay declaring a lens URL and the clock inside the peak window, the toggle removes
  the overlay (env block and owned settings keys) exactly as it does for a direct URL.
- With a lens base URL and the clock inside the peak window, `deepseek-peak-guard.sh` emits the
  `"continue": false` block.
- With a lens base URL and the clock off-peak, the guard emits nothing.
- Existing cases are unaffected: a direct `api.deepseek.com` URL behaves exactly as before, in
  both hooks, on both sides of the peak window.

## Test Specifications

- Integration Tests (`hooks/test-deepseek-auto-toggle.sh` — extend the existing harness, which
  already runs against a throwaway `HOME` with a pinned clock via `DEEPSEEK_TOGGLE_NOW`; do not
  build a second harness):
  - **Lens URL, off-peak → no write**: seed the overlay and `settings.json` with a lens base URL,
    run at `FRI_OFF`, assert `settings.json` is unchanged (compare the whole file, not just the
    env block).
  - **Lens URL, peak → overlay removed**: same seed, run at `FRI_PEAK`, assert the env block and
    the owned `settings` keys are gone.
  - **Lens URL in the sectioned overlay shape**: the same two cases against the
    `env`/`settings` shape, so the predicate is covered for both shapes the parser supports.
  - **Overlay missing**: `--no-env-file` with a lens URL → falls back to the substring rule, i.e.
    treated as `pro`. Pins the fallback rather than leaving it implicit.
  - **`${VAR}` lens URL, off-peak → no write**: an overlay declaring
    `ANTHROPIC_BASE_URL: "${LENS_URL}"` with `LENS_URL` set to the lens URL, run at `FRI_OFF` →
    `settings.json` left untouched. Fails if the predicate compares the resolved settings value
    against the unresolved placeholder.
  - **Direct URL regression**: the existing `DEEPSEEK_URL` cases, unchanged, both sides of the
    window.
- **Harness prerequisite (do this first).** The lens-URL cases need the overlay's declared
  `ANTHROPIC_BASE_URL` to be the lens URL, but `fixture` and `seed` write the overlay with a
  hardcoded `$DEEPSEEK_URL` (`test-deepseek-auto-toggle.sh:24`, `:34`). Parameterize both with the
  overlay's `ANTHROPIC_BASE_URL` so a lens-URL overlay can be seeded. (`sectioned` already takes
  the whole overlay contents as its argument, so it needs no change — pass the lens URL through
  its argument. `guard_check` reaches the overlay through `fixture`, so parameterizing `fixture`
  covers the guard cases too.) This is a harness edit, not just new cases.
- Integration Tests (`deepseek-peak-guard.sh` — new cases in the same script, or a sibling):
  - Lens URL, peak → the block JSON is emitted with `"continue": false`.
  - Lens URL, off-peak → no output.
  - Overlay missing, lens URL → no output (fallback, no match).
  - **`${VAR}`-declared overlay → not recognized**: an overlay whose `ANTHROPIC_BASE_URL` is
    `"${LENS_URL}"` with `LENS_URL` set produces no output, because the guard cannot resolve the
    placeholder. This pins the placeholder gap as a known boundary (the toggle *does* recognize
    it); update the expectation deliberately if the guard later gains a resolver.
  - `.provider-override` containing `deepseek` → no output regardless of clock (existing escape
    hatch, must survive the change).
- E2E: deferred to `/develop-tests`.

## Files to Touch

- `hooks/deepseek-auto-toggle.js` (modify — reorder the overlay read, add the shared predicate,
  cross-reference comment)
- `hooks/deepseek-peak-guard.sh` (modify — replace the substring guard with the shared predicate,
  cross-reference comment)
- `hooks/test-deepseek-auto-toggle.sh` (modify — first parameterize `fixture`/`seed` with the
  overlay's `ANTHROPIC_BASE_URL` so a lens-URL overlay can be seeded; then lens-URL cases for both
  hooks, both window sides, both overlay shapes, plus the `${VAR}` case)
