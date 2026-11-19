# Commands

`drop` below means the command line given in SKILL.md (the absolute binary path with its `--db` or `--url` option). Output is JSON on standard output (`--pretty` indents it); errors go to standard error. Exit codes: 0 ok, 1 HTTP or transport error, 2 usage, 4 conflict (HTTP 409 or a changed search generation). Never paste tokens, codes or passwords into examples or files.

## Schema

| Table | Columns |
| --- | --- |
| `apps` | `id`, `name` (1-80 characters), `slug` (unique; 3-32 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen), `created`, `updated` |
| `events` | `id`, `actor` (account id), `app`, `subject_user`, `action` (the operation, such as `invite.create`), `detail` (JSON, never secret values), `created` |

`drop schema` prints the live tables, columns and search indexes; it takes precedence over this table. Timestamps are UTC text such as `2026-10-25 09:16:27.718Z`. The project has one account, which reads every app; the account, credentials and SQLite metadata are not readable.

## add

```sh
drop add "Hello app" --slug hello-app
printf '%s\n' "$NAME" | drop add - --slug "$SLUG"
```

Adds one app (leading and trailing whitespace of the name is trimmed). Prints `{"id", "name", "slug", "created", "updated"}`. An empty or longer than 80-character name, or a missing or invalid slug, exits 2; a slug already in use exits 1. There are no update or delete commands.

## list

```sh
drop list --limit 10
```

The apps, newest first (default 50, at most 500), as an array of `{"id", "name", "slug", "created", "updated"}`.

## search

```sh
drop search "hello" --limit 10
drop search "hello" --limit 10 --offset 10 --generation GENERATION
```

Ranks the apps by full-text match of `name` and `slug`: 1-16 terms, each matched as a whole word, all required; `--limit` 1-100 (default 20). Prints `{"query", "generation", "apps", "hasMore", "nextOffset"}`; each app adds `score` and `excerpt` (from `name`). When `hasMore` is true, request the next page with `--offset` set to `nextOffset` and the same `--generation`. Exit 4 means the apps changed since the first page: discard the earlier pages and start again at offset 0 without `--generation`.

## sql

```sh
drop sql "SELECT id, name, slug, created FROM apps ORDER BY created DESC LIMIT 20"
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

Calls one operation (`POST /api/drop/op`) with a JSON object as input and prints its result. Errors print `NAME: HTTP STATUS CODE: message`; the codes are stable (`invalid_input`, `forbidden`, `not_found`, `account_exists`, and so on). A retried write with the same `--idempotency-key` and input within 24 hours replays the first result instead of acting twice; the same key with another input exits 4 (`idempotency_conflict`).

## Account and server

- `whoami`: `{"id", "url"}` of the project's account (and `db` locally); it confirms the session works. `op me` adds its email, role and quota.
- `status`: locally, paths, port, lease, write-back conflicts and `running`; with `--url`, `reachable` and `loggedIn`.
- `open [--no-browser]`: the reader. Locally it prints a single-use `signInLink` valid for 2 minutes.
- Local only: `ensure-running` (alias `start`), `stop`, `flush`, `recover --keep local|synced` (only on the user's decision), `login-code`, `password`, `init`, `serve`. `serve --hosted --headless --domain HOST --mcp` is the hosted service (`docs/hosting.md`), only when the user asks; data commands cannot reach a hosted server.
- Remote only: `login --url URL --code-stdin` (or the user's own `login --url URL` with the password), `logout --url URL`.
- `skill [--agent claude-code|codex|antigravity|all] [--dir DIR]` reinstalls this skill with the current paths.
