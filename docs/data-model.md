# Data model

Drop's first scaffold stores one placeholder collection, `apps` (a name and a unique slug). A project is single-user: one hidden account reads and writes every record, and every request must still prove it is that account. The schema lives in `pb_migrations/`; the rules below are enforced by PocketBase collection rules, by `pb_hooks/` and by the SQL and search configuration in `server.json`.

The placeholder is temporary. Drop's real model (hosted multi-account mode with invites and roles, apps with owners, releases, secrets, nodes and placements) replaces it in later milestones; no database with `1810000100_apps.js` has been released, so those milestones may replace that migration instead of adding to it.

## The account

- `users` is PocketBase's built-in auth collection, configured by `pb_migrations/1810000000_users.js`: password sign-in by email, 7-day tokens, no OAuth2, OTP or MFA. All list, create, update, delete and manage rules are closed; the account may view only its own record. The SQL reader never exposes it.
- It holds exactly one record, created by `init` through the local-key route `POST /api/bundle/account` in `pocketcontext-core` (`appserver/local.go`) (the PocketBase installer is disabled). Its email is the fixed internal address `account@drop.invalid` (`Identity.LocalAccountEmail`, mirrored as `ACCOUNT_EMAIL` in `ui/src/api.ts`); nobody chooses or sees it, and the reader and `login --url` ask only for the password. `password` changes the password, which also revokes earlier tokens. `pb_migrations/1810000200_public_password.js` adds a hidden `publicPasswordStrong` flag, set whenever a password is saved, which public mode requires at startup; every Drop password has it.
- `pb_hooks/account_access.js` refuses a second account on every path (REST, superuser, internal saves), forbids deleting the account, turns an invalid bearer token into an explicit 401 instead of an anonymous request on every route (including the SQL and search endpoints), and delivers realtime messages only to authenticated clients.
- There is no account management, no record ownership and no per-requester read filtering. The single account is still required: anonymous requests and invalid tokens get nothing.

## Apps

`pb_migrations/1810000100_apps.js` creates `apps`:

| Field | Type | Rules |
| --- | --- | --- |
| `id` | text, 15 characters | PocketBase record ID |
| `name` | text | required, at most 80 characters (the CLI and reader also trim whitespace; the CLI requires valid UTF-8) |
| `slug` | text | required, `^[a-z][a-z0-9-]{1,30}[a-z0-9]$` (3-32 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen), unique |
| `created`, `updated` | autodate | set by PocketBase |

A unique index on `slug` refuses a second app with the same slug (HTTP 400 from the records API, exit 1 from `drop add`); an index on `created` serves the newest-first list. The migration enforces the slug pattern, and the CLI (`internal/client/apps.go`) and the reader (`ui/src/api.ts`) check the same pattern before sending.

Every collection rule (list, view, create, update, delete) is `signedIn` = `@request.auth.id != '' && @request.auth.collectionName = 'users'`: the project's account may do everything, nobody else anything. Apps need no hooks; the slug is not immutable in the placeholder.

Writes go through the records API, `POST /api/collections/apps/records` with `{"name": "...", "slug": "..."}`; `drop add NAME --slug SLUG` does exactly that. The CLI and reader have no update or delete commands; the rules allow the account to update and delete apps through the API.

## SQL reads

`server.json` lists the SQL-readable tables and columns:

| Table | Columns |
| --- | --- |
| `apps` | `id`, `name`, `slug`, `created`, `updated` |

`POST /api/context/query {"sql": "SELECT ..."}` (`drop sql`) runs one read-only `SELECT` and returns `{columns, rows, truncated}`; `GET /api/context/schema` (`drop schema`) lists the readable tables, columns and search indexes with `"permissionModel": "shared"`. Both require a `users` token. The vendored SQL reader (`internal/pocketcontext/sqlread`) reads the live working database through read-only connections and authorizes every statement with an SQLite authorizer: only listed columns of listed tables, an allowlist of functions, no writes, `PRAGMA`, `ATTACH`, `sqlite_schema`, `dbstat`, `pragma_*` table-valued functions, FTS shadow tables or auth collections. Results are bounded (`maxRows` 500, `maxBytes` 1 MiB, `timeoutMs` 2000); `truncated` says a limit was hit. The column allowlist is the only read restriction on the loopback API, so list only what the account may read and never secrets.

## Search

`server.json` defines one search index:

```json
"apps": {"collection": "apps", "columns": ["name", "slug"], "snippetColumn": "name"}
```

`POST /api/context/search {"index": "apps", "query": "...", "limit": N, "offset": N, "expectedGeneration": "..."}` (`drop search`) ranks apps with SQLite FTS5 (bm25) over `name` and `slug` and returns hits with `id`, `score` and an `excerpt` of `name`, plus `generation` and `hasMore`. The index is a built index (no `table`): each request reads the indexed columns with fixed SQL in one read transaction of the live database, then searches an in-memory FTS5 index cached by a fingerprint of exactly those rows (`maxIndexBytes` and `cacheBytes` bound it). There is no FTS table in the database and nothing to maintain in migrations or hooks. Every column an index reads must be listed under `tables`. Queries take 1-16 terms, each matched as a whole word, all required, at most 4096 bytes; `limit` is 1-100 and `offset` at most 10,000. The `generation` is that fingerprint: a continuation passes it as `expectedGeneration`, and HTTP 409 (CLI exit 4) means the apps changed, so the client restarts at offset 0. The CLI reads the full records of the hits through SQL.

A built index may also restrict its rows with `"where"`, a predicate over the collection's SQL-readable columns such as `"archived = 0"` (no parameters, comments or quoted identifiers), and may be scoped by a `scope` record whose JSON object values list member IDs, with `"members": "all"` (every member must exist) or `"visible"` (members filtered out by `where` are skipped). Drop uses neither.

## Reader

The reader (`ui/`) signs in with the password, a single-use link from `open` (or `open --public` for a public origin) or a one-time code from `login-code`, lists the apps newest first through SQL, adds one (name and slug) through the records API and searches through the search endpoint. Realtime events on `apps` refresh the list. It stores its session in `localStorage` under `drop.reader.auth`.

## Public origin

Public mode is opt-in ([HTTPS](https.md), [MCP](mcp.md)). `server.json` names what a public origin may expose:

```json
"public": {"collections": ["apps"]}
```

The list fails closed: a collection that is not listed is denied on the public origin, and an auth or system collection in the list is refused at startup. Public start also refuses when any entry in `tables` is not listed, and when a listed name is not in `tables`, so the two lists hold the same collections. Realtime on the public origin accepts subscriptions to listed collections only. The list adds no identity or filter. Every public request still needs the project's one account, or an OAuth grant that account approved, and there are no per-requester filters. Collection rules still apply and must never admit anonymous requests. Local administration is not exposed. The frozen loopback route table `tests/testdata/public_routes.json` is unaffected.

## Databases from other releases

`pocketcontext-core` (`appserver/legacy.go`, `CheckDatabase`) reads the working copy before PocketBase opens it and refuses the database when its `_migrations` table records a `pb_migrations/*.js` file this binary does not embed: a database of a newer release or of another product (including the template Drop was derived from, whose collection is not `apps`). Drop has no earlier schemas (`Policy.Legacy` is empty). The file is left unchanged and the error says so. Never edit, rename or delete a migration that has shipped; add a new one.

## Adding a collection

To add a collection:

1. **Migration.** Add `pb_migrations/<timestamp>_<name>.js` with a number above the existing ones. Never edit a migration that has shipped; add a new one. Give every rule `signedIn` (`@request.auth.id != '' && @request.auth.collectionName = 'users'`), or `null` where clients must not act; never an empty rule, which would admit anonymous requests. Add indexes for the reads you expect.
2. **Hooks.** Only when the model needs them: in `pb_hooks/<name>.js` with a `<name>.pb.js` registration, set server-managed fields on create and refuse changes to immutable fields on every update path (`onRecordUpdate`, which covers REST, batch and internal saves). Hooks run in-process: no `$os` calls (`internal/appconfig/hooks_test.go` enforces it). The runtime is PocketBase's goja, not a browser or Node: there is no `URL` constructor and no `fetch`, so validate addresses with a regular expression. A `json` field's `e.record.get(name)` is the raw JSON bytes, not a parsed value (`Array.isArray` is true and `length` counts bytes): read it with `JSON.parse(String(e.record.get(name)))`, treating an empty string or `null` as unset.
3. **SQL exposure.** List the readable columns in `server.json` `tables`, never secrets or auth collections. The list is the whole read policy of the account's SQL access: there are no per-requester filters. Every entry in `tables` must also be in `public.collections` (below), or public start is refused.
4. **Search.** For full-text search add an index under `search.indexes` with the collection, its text `columns` and a `snippetColumn` (and `where` to leave rows out); every column must also be listed under `tables`. Excerpts come only from the `snippetColumn`, so a match in another column is ranked but its words may not appear in the excerpt. Add every collection in `tables` to `public.collections` in `server.json`, or public start is refused. To keep a collection off the public origin, leave it out of both lists. Never list auth or system collections.
5. **Client.** Add commands in `internal/client` (writes through `records(...)`, reads through `c.Rows`/`c.Query` and `/api/context/search`), with entries in `commandHelp` (`help.go`), the overview (`usage.go`) and the argument spec in `commands.go`; `help_test.go` checks they agree and pins each data command's output keys, so update its expected strings too. `--url` and `--db` are global flags accepted anywhere on the line (`commands.go`), so a record field with one of those names (a bookmark's url) must be positional or use another flag name.
6. **Reader, skill and tests.** Show the collection in `ui/src`, document the commands in `skills/drop`, and cover the rules (anonymous and invalid-token requests are refused, the account reads and writes), the SQL policy (unlisted columns and writes are refused) and search in `tests/e2e.py`, in `internal/appconfig/sqlite_test.go` if the table needs SQLite features, and in the browser tests.

Then run `make ui build test names identity e2e`.
