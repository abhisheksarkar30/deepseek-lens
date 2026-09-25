### Bead 15: `config` raise the body cap to 8 MiB, doctor WARN, and the README note

- **Bead ID**: br-GI-27-15
- **Priority**: P2 (medium)
- **Original Estimate**: 1h
- **Dependencies**: br-GI-27-02, br-GI-27-12
- **Blocks**: None

**Description**:

A.3. claude-lens raised its body cap to 8 MiB **in config** (its shipped default is 2 MiB). Do the same: **the shipped default stays 262144** — this is a config change plus a guard, not a default change (so the 256 KB-cap tests and docs stay true).

**1. Set the cap in the operator config** — `BodyCapBytes = 8388608` in `~/.deepseek-lens/config.toml` (or `--body-cap-bytes` / `LENS_BODY_CAP_BYTES`). State the config path in the completion note. **Do not change `Default()`** (`config.go:104-125`, `BodyCapBytes: 262144` at :111).

**2. Two facts to re-verify in code at bead time:**
- `cons.SetBodyDecoding(cfg.BodyCapBytes)` (`serve.go:115`) reuses the same value as the **decoded-size limit**, so the raise applies to both the raw capture and the decoded form. Confirm the call site and that both limits read `cfg.BodyCapBytes`.
- **Worst-case memory is `4096 × 2 × cap`** (sink capacity × both bodies per call). At 8 MiB that is 4096 × 16 MiB ≈ **64 GiB** — 32× the current bound of ~2 GiB at 256 KB. This is a structural ceiling that exists today and is **multiplied** by the cap raise.

**3. `lens doctor` WARN** when `BodyCapBytes > 262144` **and** `HotDays == 0` — large bodies, no archival, high memory ceiling. Name the ceiling explicitly (the 4096 × 2 × cap figure) so the operator can make an informed decision (`internal/cli/doctor.go`).

**4. README** — the body-cap note (README.md ~:274-275): usage survives the cap (the bead-03 tail), and the cap is configurable to 8 MiB. State the memory ceiling honestly.

**Ordering (why 02 and 12 are dependencies): do not raise the cap before archival (C) and the stats index (E) are live.** At 8 MiB the hot file grows several times faster than today's 4.7 GB store; archival bounds it and the index keeps the stats queries from reading it (§11A: they already take 22-56 s cold at today's size). E (bead 02) and C (bead 12) come first.

**Rationale**:

A.3/RC-1. The 256 KB cap is why the output-token count — the tail of an SSE stream — is dropped for 8% of rows; raising the cap fixes the bulk of the loss (the bead-03 tail is the backstop). Raising it in config rather than in the default keeps the story's blast radius to the operator's own install and leaves every 256 KB-cap test valid. The doctor WARN exists because the memory ceiling is a real, non-obvious consequence of the raise.

**Outcome Definition**:

- `~/.deepseek-lens/config.toml` sets `BodyCapBytes = 8388608`; `Default()` still returns 262144.
- `SetBodyDecoding` and the raw capture both use the raised value (verified at `serve.go:115`).
- `doctor` WARNs when `BodyCapBytes > 262144` **and** `HotDays == 0`, naming the 4096 × 2 × cap ceiling.
- `README.md` says usage survives the cap and the cap is configurable to 8 MiB, with the memory ceiling stated.
- `go build ./... && go vet ./... && go test ./...` pass (the 256 KB-cap tests are unchanged).

**Test Specifications**:

- `internal/cli/doctor_test.go`: `BodyCapBytes = 8388608` with `HotDays == 0` → WARN naming the ceiling; `BodyCapBytes = 8388608` with `HotDays > 0` → no WARN; `BodyCapBytes = 262144` → no WARN.
- `internal/config/config_test.go`: `Default().BodyCapBytes == 262144` (the shipped default is unchanged — the guard that this bead did not raise it in code).
- **Manual / recorded**: run `lens doctor` with the raised config and confirm the WARN text names the memory ceiling.

**Files to Touch**:
- `internal/cli/doctor.go` (modify — the `BodyCapBytes > 262144 && HotDays == 0` WARN)
- `internal/cli/doctor_test.go` (modify)
- `README.md` (modify — the body-cap note at ~:274-275)
- `~/.deepseek-lens/config.toml` (modify — operator config; not in the repo; state the path)
