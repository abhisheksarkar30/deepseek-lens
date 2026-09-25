### Bead 1: `chore` drop the `develop` branch from CLAUDE.md, both guards, and the context docs

- **Bead ID**: br-GI-27-01
- **Priority**: P0 (critical)
- **Original Estimate**: 1.5h
- **Dependencies**: None
- **Blocks**: br-GI-27-16 (the context-doc refresh runs last, after every bead including this one)

**Description**:

Match claude-lens: a feature branch `GI-<n>-<slug>` is cut from `main` and its PR goes **directly into `main`**. Today deepseek-lens interposes a `develop` integration branch and both guards gate on it. Change the flow (plan §11, workstream D).

**1. The two workflow files — port claude-lens's adapted versions, do not write new ones.** The source is `D:\github\claude-lens\.github\workflows\branch-guard.yml` and `main-guard.yml` (both present). Port them, then reconcile against this repo's existing checks:

- `.github/workflows/branch-guard.yml` — replace the "head must be `develop`" predicate at **:16-21** (`if [ "${{ github.head_ref }}" != "develop" ]`) with "the head matches `^GI-[0-9]+-` **and** its `GI#<n>` is an issue this PR's body closes". **Keep** the title-prefix check (:23-43), the closing-keyword check (:45-73), and the per-commit-prefix check (:75-99) exactly as they are.
- `.github/workflows/main-guard.yml` — replace the "landed via a merged `develop`→`main` PR" predicate at **:33** (`.head.ref=="develop"`) with "landed via a merged PR whose head is `GI-<n>-…`" (`.head.ref | startswith("GI-")`), and update the prose that names `develop` at **:12, :25, :36, :40, :46-47**. Keep the force-reset + auto-revert-issue behaviour.

**2. `CLAUDE.md` — three lines.** `:50` (`cut from develop` → `cut from main`); `:57` (`A feature branch PRs into develop; a separate develop → main PR promotes it` → `A story branch PRs directly into main`); `:63` (the Enforcement paragraph's `branch-guard.yml requires every PR into main to come from develop …` → claude-lens's wording, minus its "differs from deepseek-lens" note). Also update the **"Fail open"** bullet's opening sentence "The dashboard listener carries three write routes" — it becomes **five** when beads 08 and 10 add `POST /api/shutdown` and `POST /api/reload`; if those beads are not yet merged, cross-reference plan §B.2/§B.4 rather than asserting a count that does not yet hold (do not write a count the code contradicts).

**3. Context docs that describe the two-branch flow.** `docs/context/conventions.md:128`, `docs/context/testing-and-quality.md:101-102`, `docs/context/INDEX.md:122` (one occurrence). Rewrite each to the single-branch flow and its guard predicates.

**The change list is exactly the word-bounded `develop` set.** `docs/context/INDEX.md:122`, `conventions.md:128`, `testing-and-quality.md:101-102`, both workflows, and `CLAUDE.md:50,57,63`. `README.md`, `docs/context/architecture.md`, and `docs/context/build-and-run.md` carry **no** branch reference — their only match is the English word "developer" — so they are **not** in this bead's scope (bead 16 refreshes them for other reasons, not for this drop).

**Ordering / self-applying (plan §11):** a `pull_request` event runs the workflow file at the PR's merge ref — the head's version — and `main-guard.yml`'s `push` event runs the file at the pushed commit. So a PR from `GI-27-…` that carries the updated `branch-guard.yml` governs its own check; **no separate pre-PR is needed**. Deleting the remote `develop` branch is a separate, user-confirmed step and is **not** part of this bead. State verified 2026-09-25: `origin/develop` has no commit `origin/main` lacks (PR #26 promoted it), so `develop` can be retired without loss.

**Working-tree note:** an uncommitted A.1/A.2 prototype (proxy/sink/parse/consumer/store/api/stats/web) is in the tree and must be `git stash -u`-ed before bead 02 (plan §13). This bead does not touch those files, so it may proceed before the stash — but if bead 02 has already run, do **not** `git stash pop`.

**Rationale**:

D/§11. `CLAUDE.md`, both guards, and three context docs all describe a `develop` intermediate that this story retires. Leaving any of them is a repo whose written flow contradicts its own workflows. The guard change is grep-gated because the stale-file set was enumerated once (round 9) and any file it missed would silently keep the old flow documented.

**Outcome Definition**:

- `git grep -nw develop -- ':!docs/planning' ':!planning' ':!.beads' ':!docs/superpowers/specs'` returns **no matches** (word-bounded; see the gate below).
- `.github/workflows/branch-guard.yml` rejects a non-`GI-…` head into `main` and accepts a `GI-<n>-…` head whose issue the body closes; its title/closing-keyword/per-commit checks are unchanged.
- `.github/workflows/main-guard.yml` accepts a commit whose merged PR head is `GI-<n>-…` and reverts one that is not.
- `CLAUDE.md` says branches are cut from `main` and a story branch PRs directly into `main`.
- The three context docs no longer describe `develop`.
- `README.md`, `docs/context/architecture.md`, `docs/context/build-and-run.md` are **unchanged** by this bead.

**Test Specifications**:

Documentation + workflow, so verification is a grep gate with a **positive control** (a search that returns nothing *before* the change is a broken search, not a clean tree):

1. **Positive control, before editing** — confirm the pattern matches the known branch references: `git grep -nw develop` (equivalently `git grep -nE '\bdevelop\b'`) over the tree. Record the hits (both workflows, `CLAUDE.md:50/57/63`, `conventions.md:128`, `testing-and-quality.md:101-102`, `INDEX.md:122`).
2. **Prove the pattern is word-bounded** — `git grep -n develop` (unbounded) also hits the English word "developer" at `README.md:12`, `docs/context/architecture.md:11`, `docs/context/build-and-run.md:19`, `docs/context/testing-and-quality.md:113`, `docs/context/INDEX.md:7`. Those five must **never** be edited; the unbounded form can never reach zero matches, so only the `-w`/`\b`-bounded form is the gate.
3. Edit the two workflows, `CLAUDE.md`, and the three context docs.
4. Re-run the word-bounded grep with the exclusions in the outcome definition; assert zero matches. Use separate `-e` terms, never `\|` (under ripgrep `\|` is a literal pipe and matches nothing).
5. With `actionlint` (or a dry read of the YAML), confirm both workflows parse and the branch predicate reads `GI-<n>-…`, not `develop`.
6. `go build ./...` still succeeds (no Go change; the check is that nothing was touched).

**Files to Touch**:
- `.github/workflows/branch-guard.yml` (modify — the head-source step at :16-21)
- `.github/workflows/main-guard.yml` (modify — the source predicate at :33 and the prose at :12, :25, :36, :40, :46-47)
- `CLAUDE.md` (modify — :50, :57, :63; and the "Fail open" write-route count sentence)
- `docs/context/conventions.md` (modify — :128)
- `docs/context/testing-and-quality.md` (modify — :101-102)
- `docs/context/INDEX.md` (modify — :122)
