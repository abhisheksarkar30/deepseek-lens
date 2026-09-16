# Bead br-GI-17-06: `retention_days`, doctor output, and the scheduled purge

**Plan Reference**: `docs/planning/GI-17-pricing-retention-purge.md` §Design decisions D7, D8

- **Priority**: P1 (high)
- **Dependencies**: br-GI-17-05
- **Blocks**: br-GI-17-08

## Description

Retention becomes a configured threshold that the running server acts on, and `serve` becomes the
second writer D7 has to describe honestly.

**1. `RetentionDays` joins `Config`.** Field, `retention_days` file key, `LENS_RETENTION_DAYS`
environment variable, `--retention-days` flag — the same four inputs every other key has, resolved
in the established precedence (flag > env > file > default). Default `0`.

`Validate` rejects a **negative** value. That is effective for `serve` and `doctor`, which both call
`cfg.Validate()` ([serve.go:39](../../internal/cli/serve.go#L39),
[doctor.go:94](../../internal/cli/doctor.go#L94)). Note that `lens purge` calls `config.Load(nil)`
and deliberately does **not** call `Validate` — so the negative case is caught on the CLI side by
br-GI-17-08's own guard, not by this one. Both exist for that reason; neither is redundant.

**2. `0` means "keep forever", and that is the default.** This matches the repo's fail-open posture:
nothing is deleted on an unconfigured install. A default that deleted would make upgrading lens a
destructive act.

**3. `serve` purges at startup and then on a 24-hour ticker**, logging each run's deleted count.
Startup plus ticker rather than a ticker alone, because a tool that is opened and closed around
work sessions may never see a 24-hour boundary — the startup run is what makes the setting do
something on a machine that is not always on.

This runs on the **store's writer connection**, which is `SetMaxOpenConns(1)`, so it queues behind
ingest rather than racing it. It is one of the two writers D7's amended invariant must name.

**Split the run out of `Serve` so it is testable.** `Serve` cannot be driven from a test as it
stands: it loads and validates config, binds two real listeners on `cfg.ProxyAddr` /
`cfg.DashboardAddr` ([serve.go:126](../../internal/cli/serve.go#L126),
[:131](../../internal/cli/serve.go#L131)), and blocks on a `signal.NotifyContext` until interrupted.
Follow the precedent already in this file — `checkRedaction` was, in its own words, "split out from
`Serve` so the wiring is testable", because "the self-test's whole value is that it actually runs at
boot, which is not true of a function no one calls"
([serve.go:161-171](../../internal/cli/serve.go#L161-L171)). Extract:

```go
func purgeOnStartup(ctx context.Context, st *store.Store, days int, logf func(string, ...any))
```

Called once from `Serve` next to `checkRedaction`, and tested directly against a temp store — no
listener, no signal, no port. The ticker stays in `Serve`; only the run is extracted.

**4. `doctor` prints `retention_days`** in its effective-config table, alongside every other
resolved value. A retention setting that deletes rows must be visible in the one command whose job
is "what is this install actually configured to do"; a user debugging "why did my rows disappear"
should not have to read `config.toml` and reason about env-var precedence.

**5. Wire both API seams in `serve.go`.** The retention seam
(`api.SetRetention(cfg.RetentionDays, store)`) and the pricing seam
(`api.SetPricing(pricing.DefaultPath())`), next to the existing
`cons.SetPriceTable(pricing.NewLoader(pricing.DefaultPath()))`
([serve.go:86](../../internal/cli/serve.go#L86)). Both are one-liners on the value the process
already resolves — `pricing.DefaultPath()` is the same path `lens prices` writes
([prices.go:19](../../internal/cli/prices.go#L19)), which is what makes a browser save and a CLI save
land in the same file (D1).

This is the only bead that touches `serve.go`, so both seams land here; br-GI-17-03/04/07 add
handlers that read them.

## Rationale

Retention is a threshold, not a stored job (D8): there is no job table, no scheduler state, no
"next run" to persist and get wrong. The config value plus a startup run plus a ticker is the whole
feature.

The startup purge is the one irreversible thing that happens without a user present in this ticket,
which is why it is gated behind an explicitly configured positive value rather than a default, and
why its count is logged.

## Outcome Definition

- `RetentionDays` resolves flag > env > file > default; the default is `0`.
- `Validate` rejects a negative `retention_days` and names the field and value.
- `serve` with `retention_days > 0` purges once at startup and then on a 24-hour ticker, logging
  each run's deleted count.
- `serve` with `retention_days <= 0` purges nothing at startup and nothing on any tick.
- `lens doctor` prints the resolved `retention_days`.
- `serve.go` calls both `SetRetention` and `SetPricing`; with them wired, `GET /api/retention`
  reports real numbers and both price routes work against the running server.
- The scheduled purge runs on the store's writer connection — no second writer is opened in-process.

## Test Specifications

`internal/config` (`config_test.go`):

1. Precedence table for `retention_days`: file only, env overriding file, flag overriding env,
   nothing set → `0`.
2. `Validate` rejects `-1` (and a negative arriving from each of the three input paths); accepts
   `0` and a positive value.

`internal/cli` (`cli_test.go`):

3. Startup purge, against `purgeOnStartup` **directly** — not against `Serve`, which cannot be
   driven from a test (two real listeners, a blocking signal context). With `retention_days` set and
   a temp store seeded with rows past the cutoff, the run deletes exactly those rows, and delegates
   the count to `logf` so the logged line is asserted too. This is integration test 2 of the plan's
   two ("retention end to end"), and its **second** assertion is the one that catches a purge that
   deleted rows without reconciling: the `sessions` totals afterwards agree with the rows that
   remain.
4. `retention_days = 0`: the run deletes nothing, and the seeded rows are all present afterwards.
5. Doctor: the effective-config output contains the resolved `retention_days` value, asserted for a
   non-default setting so a hardcoded line would fail.

## Files to Touch

- `internal/config/config.go` (modify — `RetentionDays`, the env/flag/file key, `Validate` case)
- `internal/config/config_test.go` (modify — precedence and rejection cases)
- `internal/cli/doctor.go` (modify — the effective-config table)
- `internal/cli/serve.go` (modify — `SetRetention` + `SetPricing` wiring, startup purge, 24h ticker)
- `internal/cli/cli_test.go` (modify — startup-purge coverage, against the extracted run function)

---

## Review Notes

**`Serve` cannot be driven from a test, so the startup purge was extracted.** The bead originally
said "a serve startup deletes exactly those rows", owned by `cli_test.go` — but nothing in this repo
runs a `Serve` lifecycle. It validates config, binds two real listeners
([serve.go:126](../../internal/cli/serve.go#L126), [:131](../../internal/cli/serve.go#L131)), and
blocks on a `signal.NotifyContext` until interrupted; the only `Serve`-touching tests pass `--help`
or exercise the banner and redaction helpers directly.

The precedent is in the same file: `checkRedaction` was "split out from `Serve` so the wiring is
testable", because "the self-test's whole value is that it actually runs at boot, which is not true
of a function no one calls" ([serve.go:161-171](../../internal/cli/serve.go#L161-L171)). The bead now
extracts `purgeOnStartup(ctx, st, days, logf)` in that shape and keeps the ticker in `Serve`. As
written before, this — one of only three tests guarding the ticket's irreversible behaviour — was
more likely to be quietly downgraded to a private-helper test than implemented as stated.

