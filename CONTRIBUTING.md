# Contributing to Ollama Gateway

This guide is for contributors who want to make changes to the gateway, dashboard, or supporting docs.

For product and runtime context, start with `README.md`, `docs/plan.md`, and `docs/specs/README.md`.

## Before You Start

- Use Go 1.26 or newer. The repo Go version is defined in `go.mod`.
- Check for an existing issue before starting work.
- Use the GitHub issue templates when reporting bugs or proposing features.
- Keep behavior changes and their related docs or spec updates in the same pull request.

## Local Setup

Build the binary from the repo root:

```bash
go build -o bin/gateway ./cmd/gateway/
```

Create a local config from the example file:

```bash
cp configs/config.example.yaml configs/config.yaml
```

Then edit `configs/config.yaml` with the backends, models, admin token hash, pricing, and any local database path you want to use.

Run the gateway:

```bash
./bin/gateway --config configs/config.yaml
```

The admin dashboard is available at `http://localhost:4080/admin/` unless you change the listener address or enable TLS.

## Build and Test

Run the full test suite before opening or updating a pull request:

```bash
go test ./...
```

Also confirm the main binary still builds:

```bash
go build -o bin/gateway ./cmd/gateway/
```

While iterating locally, it is fine to run targeted package tests first, for example:

```bash
go test ./internal/auth/
go test ./internal/proxy/
```

Guidance:

- Run `gofmt` on touched Go files before submitting changes.
- Prefer small, focused changes with tests near the affected package.
- If you change HTTP behavior, auth, routing, or dashboard workflows, add or update tests that cover the changed path.

## Repo Structure

The main code paths are organized by package:

- `cmd/gateway`: startup, wiring, lifecycle, and route registration
- `internal/config`: config schema, defaults, validation, and loading
- `internal/auth`: API key and admin authentication
- `internal/ratelimit`: token-bucket rate limiting
- `internal/models`: model catalog, discovery, and resolution
- `internal/backends`: backend state, pools, and health checks
- `internal/proxy`: reverse proxying and usage extraction
- `internal/usage`: usage logging, analytics, and pricing
- `internal/dashboard`: admin handlers, templates, and static assets
- `internal/db/migrations`: database migrations

If you are not sure where a behavior belongs, read `docs/plan.md` and the relevant file under `docs/specs/` before changing code.

## Documentation and Specs

The repo uses `docs/specs/` for behavior and architecture contracts.

- Use `docs/specs/README.md` for the recommended reading order.
- Update the relevant spec when you change externally visible behavior, request flow, config semantics, persistence rules, or dashboard behavior.
- Update `README.md` when user-facing setup, packaging, or operational behavior changes.

Common examples:

- Auth or rate limiting changes: update `docs/specs/03-auth-rate-limiting-spec.md`
- Model routing changes: update `docs/specs/04-model-management-spec.md` or `docs/specs/06-backend-routing-spec.md`
- Config changes: update `docs/specs/04-config-spec.md` and `configs/config.example.yaml`
- Persistence changes: update `docs/specs/05-data-model-spec.md` and migration files
- Dashboard behavior changes: update `docs/specs/07-dashboard-ui-spec.md`

## Issues

Use the repository issue templates:

- Bug reports should include the affected area, deployment style, reproduction steps, and observed behavior.
- Feature requests should include the problem statement, proposed change, scope, and acceptance criteria.

If your change is tied to an issue, link it in the pull request.

## Pull Requests

Pull requests should follow the existing PR template.

Expected baseline:

- Link the tracked issue with `Fixes #...` when applicable.
- Summarize what changed and why.
- Run `go test ./...`.
- Run `go build -o bin/gateway ./cmd/gateway/`.

Add extra verification when it applies:

- Dashboard changes: include screenshots or a short recording.
- API behavior changes: verify status codes and error payloads.
- Packaging changes: run `./scripts/build-packages.sh`.

## Change-Specific Expectations

Some changes require updates outside the immediate code path.

### Config changes

- Update `configs/config.example.yaml`.
- Update the relevant config docs and defaults.
- Call out compatibility or rollout impact in the PR.

### Database schema changes

- Add a new migration under `internal/db/migrations/`.
- Document upgrade or rollback notes in the PR.
- Update persistence docs if behavior changes.

### Auth, security, or rate-limit changes

- Validate admin token flow, API key handling, and rate-limit behavior.
- Update the auth/rate-limit spec if request behavior changes.

### Model routing or backend selection changes

- Validate backend selection, failover, and user model restrictions.
- Update the model-management or routing specs when behavior changes.

### Dashboard or UI changes

- Verify the affected page in a browser.
- Include visual evidence in the PR when the UI changes materially.
- Keep templates, handlers, and static assets consistent.

### Packaging or install changes

- Update `packaging/nfpm.yaml` and any affected lifecycle scripts or systemd units.
- Run `./scripts/build-packages.sh` if the packaging flow changed.

## Optional Packaging Workflow

If your change affects Linux packaging, install `nfpm` and build packages locally:

```bash
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest
./scripts/build-packages.sh
```

Package artifacts are written to `bin/packages/`.

If your change affects Windows MSI packaging, build it locally with WiX Toolset v7.0.0+:

```powershell
./packaging/scripts/build-packages.ps1
```

Windows packaging notes:

- Requires the `wix` .NET CLI tool
- Produces MSI artifacts in `bin/packages/`
- Uses installer helper `cmd/installer-bootstrap` to generate `config.yaml` and bootstrap token output during install

## Review Guidance

Make reviewer work easier:

- Keep changes scoped to one concern when possible.
- Mention risky paths, edge cases, or tradeoffs in the PR notes.
- Call out any follow-up work you intentionally left out.

Thanks for keeping code, tests, and docs aligned.
