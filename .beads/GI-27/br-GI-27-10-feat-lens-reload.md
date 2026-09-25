### Bead 10: `feat` `POST /api/reload` + `lens reload`, with the `RetentionDays` live-apply arm

- **Bead ID**: br-GI-27-10
- **Priority**: P1 (high)
- **Original Estimate**: 2h
- **Dependencies**: br-GI-27-08
- **Blocks**: br-GI-27-14

**Description**:

B.4. Reload the config of a running `serve` without a restart: apply the fields that can change live, report the rest as needing a restart. Plan §5 B.4. This bead lands the route, the client, and the **`RetentionDays` arm** only; bead 14 adds the `HotDays` arm to the same struct.

**1. `POST /api/reload` (`internal/api/api.go`).** Same two guards as shutdown (bead 08): `replayOriginReject` (`api.go:711`) parameterized by action plus the loopback-caller check. The handler re-runs `config.Load(bootArgs)` where **`bootArgs` is the translated post-subcommand slice `serve` received** — the exact slice passed to `config.Load` at `serve.go:35`, i.e. `translateNoCapture(os.Args[2:])` (`serve.go:271`), **not** the raw `os.Args[2:]` and **not** the state file's `args`.
- Raw args would break a `--no-capture` instance: `translateNoCapture` rewrites the alias to `--capture=false` (F6.3), and `config.Load`'s closed flag set rejects the unknown `--no-capture` with a 400, making a valid instance unreloadable.
- The state file's `args` are out too: `flag.Parse` stops at the first non-flag, so `["serve","--x"]` would drop every flag and reload would diff against defaults.
- The handler validates the reloaded config and diffs it against the boot config.

**2. The live-apply mechanism — one small `atomic`-guarded struct.** Create a struct (e.g. `runtimeConfig` / `liveConfig`) holding the live-appliable fields, read atomically by the purge/archive ticker and by the API.
- This bead puts **`RetentionDays`** in it and routes the API's own `retentionDays int` copy (`api.go:99`, `SetRetention` at `:120`) **through the struct** — after a reload, the copy must also be updated through the same struct, **not set independently**, so `GET /api/retention` and a concurrent purge see the new value (the copy backs `GET /api/retention` and `POST /api/purge`).
- **Design the struct to take `HotDays` as a second field** — bead 14 adds it. Do **not** add `HotDays` here.
- Prices already hot-reload through `pricing.Loader`; there are no accounts here, so claude-lens's `Accounts` arm does not port.

**3. `lens reload`** (new `internal/cli/reload.go`; register in `cmd/lens/main.go:15-28`). Strip any own flags before `config.Load` (same discipline as beads 08/09), find the running serve through the state file / health, `POST /api/reload`, and print the response.

**4. Response shape.** `{"applied":[…],"restart_required":[…],"unchanged":bool}`. An invalid file changes nothing and returns **400 — all-or-nothing**.

- **Live-apply set (this bead):** `RetentionDays`. (Bead 14 adds `HotDays`.)
- **Everything else that differs → `restart_required`:** addresses, upstream, DB path, body cap/policy, capture flag, calendar keys.

**5. Security docs.** Add `POST /api/reload` to `docs/context/api-surface.md` and `docs/context/security-and-permissions.md` (the write-route table grows to five once this and bead 08's shutdown land).

**Rationale**:

B.4. Retention is a policy an operator changes without wanting to drop the proxy for a running coding session; prices already reload live, so an inconsistency (prices live, retention not) is the trap. The translated-args requirement is the difference between a reload that works and one that 400s a valid `--no-capture` instance. All-or-nothing prevents a half-applied config on a bad file.

**Outcome Definition**:

- `POST /api/reload` re-runs `config.Load` on the **translated** boot args; a `--no-capture` instance reloads (the `--capture=false` alias round-trips) rather than 400-ing.
- `RetentionDays` is held in one atomic-guarded struct; the API's `retentionDays` copy and `SetRetention` route through it; `GET /api/retention` reflects the new value after a reload.
- The response is `{"applied":[…],"restart_required":[…],"unchanged":bool}`; an invalid file returns 400 and applies nothing.
- `POST /api/reload` and `lens reload` reject a non-loopback caller and a cross-origin `Origin` (403) and accept loopback.
- `lens reload` strips its own flags before `config.Load`.
- `go build ./... && go vet ./... && go test ./...` pass.

**Test Specifications** (`internal/api/api_test.go`, `internal/cli/reload_test.go`):

- Reload applies `RetentionDays`; `GET /api/retention` reflects the new value after the reload (the API-copy arm, F3.7).
- Reload on an **invalid** file changes nothing and returns 400 — assert the old `RetentionDays` is still in force.
- A `--no-capture` instance reloads (the translated `--capture=false` alias round-trips) rather than 400-ing (F6.3).
- A non-loopback caller → 403; a cross-origin `Origin` → 403; loopback → accepted.
- `applied`/`restart_required` classification: a changed `RetentionDays` and an unchanged `HotDays` land in `applied`/`unchanged`; a changed address/body-cap/capture flag lands in `restart_required`.
- `lens reload --<unknown>` does not fail `config.Load` (its own flags are stripped first).

**Files to Touch**:
- `internal/api/api.go` (modify — `POST /api/reload`; route `retentionDays`/`SetRetention` at :99/:120 through the atomic struct; store `bootArgs`)
- `internal/api/api_test.go` (modify)
- `internal/api/livecfg.go` (create — the atomic-guarded live-config struct, designed for a second `HotDays` field)
- `internal/cli/reload.go` (create — `lens reload`)
- `internal/cli/reload_test.go` (create)
- `internal/cli/serve.go` (modify — pass `translateNoCapture(args)` to the API so reload can re-run it)
- `cmd/lens/main.go` (modify — register `reload` at :15-28)
- `docs/context/api-surface.md`, `docs/context/security-and-permissions.md` (modify — the new guarded route)
