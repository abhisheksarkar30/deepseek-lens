# Bead br-GI-4-08: Plugin README documents the proxy-fronted setup

**Plan Reference**: `docs/planning/GI-4-peak-cost-and-hook-integration.md` §4.1, §8.2

**Repo**: `D:\github\agentic-ai-artifacts` — the text lands in the **other** repository. This bead
file lives here as the story's audit trail.

- **Priority**: P2 (medium)
- **Dependencies**: br-GI-4-07
- **Blocks**: none

## Description

br-GI-4-07 makes both hooks recognize a DeepSeek route that is fronted by a local proxy — but only
when the overlay `~/.claude/.deepseek-env.json` declares the **proxy's** URL rather than DeepSeek's
own. Nothing in the repo says so, and getting it wrong is silent: the peak guard does not fire and
the toggle overwrites the proxy URL on the next off-peak launch.

Add a short subsection to the plugin README's "DeepSeek / Claude Pro auto-toggle" section covering:

1. **What to put in the overlay when a local proxy fronts DeepSeek.** Point `ANTHROPIC_BASE_URL`
   at the proxy (for lens, `http://127.0.0.1:8787`) instead of `https://api.deepseek.com/anthropic`.
   The overlay is the machine's statement of what the DeepSeek route looks like, and the hooks read
   it to decide whether a session is DeepSeek-backed — a non-DeepSeek-looking URL there is expected
   and correct.

2. **Why the substring rule alone is not enough**, in one sentence: the hooks used to decide by
   looking for `deepseek` in the base URL, which a `127.0.0.1` URL never contains, so a
   proxy-fronted session was invisible to both.

3. **The escape hatch is unchanged**: `.provider-override` containing `deepseek` still pins the
   provider and still makes the peak guard step aside entirely.

4. **Confirming it works**: with a proxy-fronted setup, `lens doctor` prints a `provider_hooks` row
   reporting whether the peak guard will recognize this route (evaluating the **same predicate the
   hooks use** — effective `ANTHROPIC_BASE_URL` contains `deepseek`, else equals the overlay's
   declared value), so a proxy-fronted setup whose **present** overlay declares the wrong address
   warns.

5. **If Claude Code's config directory is relocated.** `CLAUDE_CONFIG_DIR` moves `settings.json`,
   the overlay and `.provider-override` together, and br-GI-4-07 makes both hooks honor it — so the
   hooks follow the user. The *manual* steps this README documents do not: where to copy
   `deepseek-key.ps1`, and the absolute `apiKeyHelper` path inside the overlay, are literals the
   user writes and must rewrite when the directory moves. One sentence, placed with those steps
   rather than in the proxy subsection, so it lands where someone following them will read it
   (plan §8.6).

Also fold in the operational note that already exists in the README but is easy to miss when
someone is editing these files — hook changes ship in the plugin bundle, so `hooks/` needs
`/plugin marketplace update agentic-ai-artifacts` followed by uninstall + install (there is no
`--force`), unlike skills and commands which are read live. Restate it next to the new subsection
so a reader who came for the proxy setup does not miss it.

## Rationale

The whole risk of br-GI-4-07 is a correct fix that never engages because the user's overlay still
declares the direct URL (§8.2 of the plan). Documentation is the only thing that closes that gap
before it bites, and the plugin README is where a user setting up the overlay is already reading.

The reinstall note is repeated here for the same reason it exists at all: a committed hook change
that was never reinstalled looks exactly like a hook that does not work.

## Outcome Definition

- The plugin README's auto-toggle section contains the proxy-fronted setup, with lens named as the
  concrete example and its default address given.
- The overlay example for the proxy case is present and shows `ANTHROPIC_BASE_URL` pointing at the
  proxy.
- The statement that the overlay's URL is what the hooks recognize is explicit — a reader must not
  have to infer it from the code.
- The `.provider-override` behaviour is stated as unchanged.
- The relocated-config-directory caveat is present, and says which steps follow `CLAUDE_CONFIG_DIR`
  (the hooks) and which do not (the manual copy and the absolute `apiKeyHelper` path).
- The hook-reinstall requirement is restated in or beside the new subsection.
- No claim contradicts `hooks/deepseek-auto-toggle.js` or `hooks/deepseek-peak-guard.sh` as they
  stand after br-GI-4-07.

## Test Specifications

- No code tests. Verification is a read-through: every URL and file path named in the new prose
  must exist as written (the overlay path, the `--set`-style example, the lens default proxy
  address `127.0.0.1:8787` as it appears in `internal/config/config.go`).
- Cross-check the two hook files' comments against the README's description of the predicate —
  all three must describe the same rule.

## Files to Touch

- `README.md` (modify — the "DeepSeek / Claude Pro auto-toggle" section)
