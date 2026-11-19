# Commands

`drop` below means the command line given in SKILL.md (the absolute binary path with its `--db` or `--url` option). Output is JSON on standard output (`--pretty` indents it); errors go to standard error. Exit codes: 0 ok, 1 HTTP or transport error, 2 usage, 4 conflict (HTTP 409 or a changed search generation). Never paste tokens, codes or passwords into examples or files.

## Schema

| Table | Columns |
| --- | --- |
| `apps` | `id`, `owner`, `name` (1-80 characters), `slug` (unique; 3-32 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen), `nib_app_id`, `hostname`, `desired_state`, `idle_timeout_ms`, `vcpu`, `memory_mib`, `disk_mib`, `http_port`, `executable_name`, `health_kind`, `health_path`, `deployment_seq`, `current_release`, `archived`, `archived_at`, `created`, `updated` (no environment or arguments: read those with `app show`) |
| `app_secret_names` | `id`, `app`, `name`, `ssm_version`, `length`, `set_via` (`ui` or `mcp`), `updated_by`, `created`, `updated`: never a value |
| `releases` | `id`, `app`, `sha256`, `size_bytes`, `s3_key` (`releases/<sha256>`), `note`, `created_by`, `created` |
| `events` | `id`, `actor` (account id), `app`, `subject_user`, `action` (the operation, such as `invite.create`), `detail` (JSON, never secret values), `created` |

`drop schema` prints the live tables, columns and search indexes; it takes precedence over this table. Timestamps are UTC text such as `2026-10-25 09:16:27.718Z`. SQL reads whole tables with the project's own account (an admin); the accounts, credentials, secret values and SQLite metadata are not readable.

## app

```sh
drop app list [--all] [--archived] [--limit N]
drop app show APP
drop app create NAME|- --slug SLUG [--mode running|on-request|stopped] [--idle-timeout MS] [--vcpu N] [--memory MIB] [--disk MIB] [--port N] [--executable NAME] [--args-json JSON] [--health tcp|http] [--health-path PATH]
drop app set APP [--name NAME] [the same settings]
drop app env APP KEY=VALUE|-KEY ...
drop app start|stop|archive|restore APP
```

APP is an app's id or slug; another account's app is "not found" (exit 1).

- An app: `id`, `name`, `slug`, `owner`, `owner_email`, `nib_app_id` (`d-<id>`), `volume_id`, `hostname` (`SLUG.<app domain>`, empty without one), `desired_state`, `idle_timeout_ms` (60000-86400000), `vcpu` (1-2), `memory_mib` (128-4096), `disk_mib` (256-16384), `http_port` (1024-65535), `executable_name` (`^[a-z][a-z0-9-]{0,31}$`), `args`, `env` (plain values only), `health_kind` (`tcp` or `http`), `health_path`, `deployment_seq`, `deployment_id` (`<nib_app_id>-dep-<deployment_seq>`), `current_release` (a release id, empty before the first deploy), `archived`, `archived_at`, `created`, `updated`.
- `app list` prints `{"apps", "has_more", "quota"}` newest first; `quota` is `{"used", "limit", "unlimited"}`. `--archived` lists archived apps; `--all` (admins) every account's.
- `app create` defaults: `on-request`, 300000 ms, 1 vCPU, 512 MiB, 1024 MiB disk, port 8080, executable `server`, a tcp health check (`--health http` checks `/` unless `--health-path` says otherwise). Prints `{"app"}`. A taken slug (`slug_taken`) or a full quota (`quota_exceeded`) exits 4; a reserved slug (`drop`, `www`, `api`, ...) or a bad value exits 1 with the field.
- `app set` prints `{"app", "deployment_bumped"}`; the slug never changes. A changed state, port, executable, arguments, environment, health check, vCPU, memory or idle timeout starts a new deployment (`deployment_bumped: true`); the name, `app start`/`app stop` and the disk do not.
- `app env` sets `KEY=VALUE` and removes `-KEY`. Names are `^[A-Z][A-Z0-9_]{0,63}$`, not `HOME`, `PATH`, `NIBRUN_*` or `DROP_*`, at most 64 together with the secrets; values at most 4096 bytes and may use only `${NIBRUN_HOSTNAME}` and `${NIBRUN_HTTP_PORT}` among `${NIBRUN_*}` references.
- `app start` returns a stopped app to the state it had (running or on-request). `app archive` stops it and frees a quota place (data, secrets and slug stay); an archived app cannot change until `app restore` (which needs a free place). Writes to an archived app exit 4 (`app_archived`).

## secret

```sh
drop secret list APP
printf '%s' "$VALUE" | drop secret set APP NAME
drop secret delete APP NAME
```

- `secret set` reads the value (1-4096 bytes; one trailing newline removed) from standard input only, stores it in the secret store as a new version and starts a new deployment. Prints `{"secret", "deployment_bumped", "deployment_id"}`. A name that is a plain variable of the app is refused; delete it with `app env APP -NAME` first.
- A secret: `name`, `version`, `length`, `set_via` (`ui` or `mcp`), `updated_by`, `updated`. No command, operation or log ever returns a value.
- `secret delete` removes every version and starts a new deployment.
- A server without a secret store (the local development server) answers `secrets_unavailable` (exit 1); a store that fails answers `secret_store_failed` (exit 1) and nothing changes.

## release

```sh
drop release list APP [--limit N]
drop release upload APP FILE [--activate] [--note TEXT]
drop release deploy APP RELEASE_ID
```

- FILE must be a static linux/amd64 ELF executable of at most 90 MiB; anything else exits 2 before anything is sent. `release upload` asks for a one-time upload link (`release.upload_link`, valid 15 minutes) and sends the file to it; it prints `{"release", "created", "deployed", "deployment_id"}` (`created` is false when the app already had these bytes). `--activate` deploys it (a new deployment); `--note` keeps a short note (at most 500 characters).
- A release: `id`, `app`, `sha256`, `size_bytes`, `s3_key` (`releases/<sha256>`), `note`, `created_by`, `created`, `current`.
- `release list` prints `{"releases", "has_more", "current_release"}` newest first. `release deploy` prints `{"app", "release", "deployment_bumped", "deployment_id"}`; deploying the current release changes nothing.
- A server without a release store (the local development server) answers `releases_unavailable` (exit 1). Without the CLI, `drop op release.upload_link '{"app":"APP"}'` returns a `url` for `curl -fT FILE URL` from any machine; treat it as a secret.

## search

```sh
drop search "hello" --limit 10
drop search "hello" --limit 10 --offset 10 --generation GENERATION
```

Ranks the apps by full-text match of `name` and `slug`: 1-16 terms, each matched as a whole word, all required; `--limit` 1-100 (default 20). Prints `{"query", "generation", "apps", "hasMore", "nextOffset"}`; each app adds `score` and `excerpt` (from `name`). When `hasMore` is true, request the next page with `--offset` set to `nextOffset` and the same `--generation`. Exit 4 means the apps changed since the first page: discard the earlier pages and start again at offset 0 without `--generation`.

## sql

```sh
drop sql "SELECT id, name, slug, desired_state, deployment_seq, created FROM apps ORDER BY created DESC LIMIT 20"
drop sql "SELECT count(*) AS n FROM apps WHERE name LIKE '%hello%'"
drop sql "SELECT strftime('%Y-%m-%d', created) AS day, count(*) AS n FROM apps GROUP BY day ORDER BY day"
drop sql "SELECT id, name FROM apps WHERE slug = 'hello-app'"
printf '%s\n' "SELECT id FROM apps LIMIT 5" | drop sql -
```

One read-only `SELECT` (alias `query`). Prints `{"columns", "rows", "truncated"}`; rows are arrays in column order. Results are bounded (500 rows, 1 MiB): when `truncated` is true a warning is printed; narrow the query or page with `ORDER BY` and `LIMIT`/`OFFSET`. Writes, `PRAGMA`, `sqlite_schema` and unlisted tables or columns are rejected with HTTP 400 (exit 1).

## Invites and accounts (admins)

```sh
drop invite create friend@example.com [--role user|admin] [--quota N] [--expires-days N]
drop invite list [--state all|open] [--limit N]
drop invite revoke INVITE_ID
drop user list [--limit N]
drop user set USER_ID|EMAIL [--role user|admin] [--quota N] [--suspend | --unsuspend]
```

- `invite create` lower-cases the address (plain ASCII, at most 254 characters); role `user` by default, app quota 3 (0-100), valid 7 days (1-30). Prints `{"invite", "sign_in_url"}`; no email is sent, so the sign-in URL is shared by hand. An address with an account (`account_exists`) or an open invite (`invite_exists`) exits 4.
- An invite has `id`, `email`, `role`, `app_quota`, `state` (`open`, `used`, `revoked` or `expired`), `invited_by`, `expires_at`, `used_at`, `used_by`, `revoked_at`, `created`. `invite list` prints `{"invites", "has_more"}`, newest first. `invite revoke` closes an open invite (`invite_closed`, exit 4, otherwise).
- An account has `id`, `email`, `role`, `app_quota`, `suspended`, `owner`, `signed_in`, `invited_by`, `created`. `user list` prints `{"users", "has_more"}`; `user set` prints `{"user"}`. Suspending ends the account's sessions at once. Nobody changes their own role or suspension, and the owner is always an unsuspended admin (HTTP 403, exit 1).
- These commands need an admin. Against a hosted server on this machine they act as its owner through its private socket.

## op

```sh
drop op me
drop op invite.list '{"state":"open"}'
printf '%s' "$INPUT_JSON" | drop op invite.create - --idempotency-key "$KEY"
```

Calls one operation (`POST /api/drop/op`) with a JSON object as input and prints its result. Errors print `NAME: HTTP STATUS CODE: message`; the codes are stable (`invalid_input`, `forbidden`, `not_found`, `account_exists`, `quota_exceeded`, and so on). `secret.set` ignores `--idempotency-key` (no trace of a value is kept); setting it again is safe. A retried write with the same `--idempotency-key` and input within 24 hours replays the first result instead of acting twice; the same key with another input exits 4 (`idempotency_conflict`).

## Account and server

- `whoami`: `{"id", "url"}` of the project's account (and `db` locally); it confirms the session works. `op me` adds its email, role and quota.
- `status`: locally, paths, port, lease, write-back conflicts and `running`; with `--url`, `reachable` and `loggedIn`.
- `open [--no-browser]`: the reader. Locally it prints a single-use `signInLink` valid for 2 minutes.
- Local only: `ensure-running` (alias `start`), `stop`, `flush`, `recover --keep local|synced` (only on the user's decision), `login-code`, `password`, `init`, `serve`. `serve --hosted --headless --domain HOST --mcp` is the hosted service (`docs/hosting.md`), only when the user asks; `search` and `sql` cannot reach a hosted server; the `app`, `secret`, `invite`, `user` and `op` commands reach one on this machine through its private socket.
- Remote only: `login --url URL --code-stdin` (or the user's own `login --url URL` with the password), `logout --url URL`.
- `skill [--agent claude-code|codex|antigravity|all] [--dir DIR]` reinstalls this skill with the current paths.
