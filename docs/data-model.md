# Data model

Drop stores accounts with roles (`users`), `invites`, `apps`, the names of their secrets (`app_secret_names`; values live only in SSM), their `releases` (the executables live in S3), one-time `upload_links`, `events` and the private idempotency keys of its operations (`op_keys`). A hosted Drop holds several accounts (the owner and the people an admin invited; see [Hosting](hosting.md)); each account sees and changes only its own apps, and admins see every app. A local project, for developing the reader, keeps exactly one hidden account, an admin. Every request must prove which account it is. The schema lives in `pb_migrations/`; the rules below are enforced by PocketBase collection rules, by `pb_hooks/`, by Go hooks and operations in `internal/dropserver` and by the SQL and search configuration in `server.json`. Drop writes through its operation route, release uploads, node reports, account hooks and maintenance jobs. Collection write rules are closed. See [Operations](ops.md).

`1820000200_apps.js` replaced the unreleased placeholder `1810000100_apps.js` (an owner, a name and a slug) before any database had shipped. Migration `1820001300_secret_cleanup.js` adds the private SQLite table `drop_secret_deletions` for outstanding SSM deletions and removes the shared object store’s deduplication flag from historical upload events. The queue holds parameter names only, and Drop exposes it through neither the collection API nor the SQL reader. Back up the project before upgrading. To roll back, restore the pre-upgrade backup and account for external parameter changes made since the backup.

Migration `1820001700_usage.js` (0.3.0) adds S3 usage metering: the collections `app_usage_hourly` and `app_usage_daily` ([Usage](#usage)), the apps fields `usage_limits` and `replica_sync_seconds`, and three private SQLite tables of the collector (`drop_usage_log_objects`, `drop_usage_state`, `drop_usage_alerts`). Existing apps keep the default limits and no sync interval (their Crew guest keeps its own default). Back up the project before upgrading; rolling back needs the backup.

Migration `1820001400_app_http_port.js` lowers the minimum guest HTTP port to 1. It preserves existing app settings and deployment numbers. Back up the control database before upgrading so it can be restored if you roll back to 0.1.0.

## The account

- `users` is PocketBase's built-in auth collection, configured by `pb_migrations/1810000000_users.js` (password sign-in by email locally, 7-day tokens, no OAuth2, OTP or MFA) and `1820000000_drop_users.js`, which adds Drop's fields and rules:

  | Field | Type | Rules |
  | --- | --- | --- |
  | `role` | select `admin` or `user` | required; set at creation (below), changed only by `user.set` |
  | `app_quota` | number 0-100 | the invite's quota (default 3); admins are exempt; changed by `user.set` |
  | `suspended` | bool | changed by `user.set`; a suspended account cannot sign in or renew a session |
  | `invited_by` | relation to `users` | the admin whose invite created the account |

  The list rule and every create, update, delete and manage rule are closed; an account may view only its own record (`@request.auth.collectionName = 'users' && @request.auth.suspended = false && id = @request.auth.id`), and users records are never a public route of the hosted origin. Admins list accounts with the `user.list` operation. The auth rule is `suspended = false`. The SQL reader never exposes the collection.
- **Hosted** (`serve --hosted`, `Policy.Accounts` = `AccountsMulti`): core switches the collection to email sign-in (one-time codes and links, password sign-in off, 30-day sessions) and creates accounts itself: the owner (`DROP_OWNER_EMAIL`) on `--hosted-init`, and any other address only when `Hooks.AllowSignIn` admits it. Drop is invite-only: `AllowSignIn` (`internal/dropserver/accounts.go`) admits an address only with an open (unused, unrevoked) and unexpired invite. Core prunes accounts that never signed in (unverified) after 15 minutes. `Runtime.IsOwner` tells the owner apart.
- **Account creation** (`OnRecordCreate("users")`, model level, so it covers core's creation and every other save): the owner's address (captured from the hosted configuration before anything is created) and, locally, the local account get `role = admin`; any other address needs an open invite, else the save is refused (defence in depth behind `AllowSignIn`), and takes the invite's `role` and `app_quota`, with `invited_by`. Every account gets `emailVisibility = true` and `suspended = false`, and the invite is marked used (`used_at`, `used_by`). When core prunes an account that never signed in, its invite opens again (unless the address has a newer open invite), so the person can still sign in before it expires. At every start the owner is made an admin again.
- **Local**: one record, created by `init` through the local-key route `POST /api/bundle/account` in `pocketcontext-core` (`appserver/local.go`) (the PocketBase installer is disabled). Its email is the fixed internal address `account@drop.invalid` (`Identity.LocalAccountEmail`, mirrored as `ACCOUNT_EMAIL` in `ui/src/api.ts`); nobody chooses or sees it, and the reader and `login --url` ask only for the password. It is an admin. `password` changes the password, which also revokes earlier tokens. Core refuses to serve locally a database that has held hosted accounts, and to serve hosted a database that holds the local account.
- `pb_hooks/account_access.js` accepts more than one account (core decides which exist), lets core delete an account that never signed in but refuses to delete one that has (on every path: REST, superuser, internal saves; suspend it instead), turns an invalid bearer token into an explicit 401 instead of an anonymous request on every route (including the SQL and search endpoints), and delivers realtime messages only to authenticated clients.

## Invites

`pb_migrations/1820000100_invites.js` creates `invites`, written only by the `invite.*` operations and the account hooks:

| Field | Type | Rules |
| --- | --- | --- |
| `email`, `email_lower` | text, at most 254 | the canonical (trimmed, lower-cased ASCII) address |
| `role` | select `admin` or `user` | the role the account gets |
| `app_quota` | number 0-100 | the quota the account gets |
| `invited_by` | relation to `users` | required: the admin who invited |
| `expires_at` | date | required: 1-30 days after creation (default 7) |
| `used_at`, `used_by` | date, relation to `users` | set when the account is created |
| `revoked_at` | date | set by `invite.revoke`, or when an expired invite is replaced |
| `created`, `updated` | autodate | |

A partial unique index allows one open invite per address (`email_lower` where `used_at` and `revoked_at` are empty); used and revoked invites stay as history. List and view rules: admins only (`@request.auth.collectionName = 'users' && @request.auth.suspended = false && @request.auth.role = 'admin'`); writes closed.

## Events

`pb_migrations/1820000900_events.js` creates `events`, one row per change made through an operation (and, later, the agent routes): `actor` (relation to `users`), `app` (relation to `apps`), `subject_user` (relation to `users`), `action` (the operation, such as `invite.create`), `detail` (JSON, at most 8 KiB, never a secret value) and `created`. Indexes on `(app, created)` and `(created)`. List and view rules: admins see every event, and an account the events of its own apps (`app.owner = @request.auth.id`); writes closed. `1820001000_nodes.js` adds `actor_node` (relation to `nodes`): the node behind an event of the agent routes (`app.phase`), whose `actor` is empty.

## Idempotency keys

`pb_migrations/1820000950_op_keys.js` creates `op_keys`, private to the server (every rule closed, never a public route or an SQL table): `key_hash` and `input_hash` (SHA-256, hidden), `op`, `result` (hidden JSON, empty for results shown only once), `stored` and `created`. A unique index on `key_hash`. Rows expire after 24 hours and are pruned hourly; see [Operations](ops.md#idempotency-keys).

## Apps

`pb_migrations/1820000200_apps.js` creates `apps`, written only by the `app.*` and `secret.*` operations (`internal/dropserver/ops_apps.go`, `ops_secrets.go`):

| Field | Type | Rules |
| --- | --- | --- |
| `owner` | relation to `users` | required; the account that created the app; never changes |
| `crew_owner_email` | text, at most 254 | Verified hosted account email copied when creating a Crew app. Empty on ordinary apps. Its value fixes the app kind and the Crew owner through moves, redeployments and restores. |
| `sign_in_email_enabled` | bool | Enables managed delivery for a Crew app. Set at creation and editable through `app.set`. |
| `name` | text, at most 80 | required |
| `slug` | text | required, `^[a-z][a-z0-9-]{1,30}[a-z0-9]$`, unique across all accounts, not a reserved name; immutable |
| `nib_app_id` | text | `d-<record id>`: nibrunner's app id; unique, immutable |
| `volume_id` | text | `d-<record id>-vol`; immutable |
| `hostname` | text | `<slug>.<DROP_APP_DOMAIN>` at creation, empty when the server has no app domain |
| `desired_state` | select `running`, `on-request`, `stopped` | required |
| `start_state` | select `running`, `on-request` | required: the state `app.start` returns to (the last non-stopped state) |
| `idle_timeout_ms` | number 60000-86400000 | required (default 300000); the sleep timeout of an on-request app |
| `vcpu` | number 1-2 | required (default 1) |
| `memory_mib` | number 128-4096 | required (default 512) |
| `disk_mib` | number 256-16384 | required (default 1024); fixed once placed (`first_placed_at`) |
| `hook_port` | integer 0–65535 | 0 disables hooks. A nonzero value must differ from the HTTP port. |
| `crew_enrollment` | select | New Crew apps start `fresh`; the owner can finish enrollment to transition to `established`. Empty on ordinary and pre-upgrade apps. |
| `http_port` | integer 1-65535 | required (default 8080) |
| `executable_name` | text | required, `^[a-z][a-z0-9-]{0,31}$` (default `server`): the file `/app/<name>` |
| `args` | json, at most 4 KiB | a list of strings |
| `env` | json, at most 32 KiB | plain environment variables (never secrets) |
| `health_kind`, `health_path` | select `tcp`, `http`; text | required kind; a path only for `http` |
| `deployment_seq` | number 1-999999999 | required: the deployment number, bumped when the instance configuration changes ([Operations](ops.md#deployments)) |
| `current_release` | relation to `releases` | the release the app runs, set by `release.deploy` or an upload link with `activate` (added by `1820000300_releases.js`); empty before the first deploy |
| `archived`, `archived_at` | bool, date | set by `app.archive`, cleared by `app.restore` |
| `first_placed_at` | date | set by the first `placement.set` (added by `1820001100_placements.js`): from then on the disk cannot change |
| `usage_limits` | json, at most 1 KiB | an admin's overrides of the app's monthly soft limits, `{requests_a, requests_b, bytes_out, stored_bytes}` (zero or absent keeps the default); written by `usage.set_limits` (added by `1820001700_usage.js`) |
| `replica_sync_seconds` | number 0-300 | a Crew app's Litestream sync interval, handed to the guest as `DROP_REPLICA_SYNC_INTERVAL`; new Crew apps get 10, 0 leaves the guest's default; written by `app.set` (`replica_sync_interval`) |
| `created`, `updated` | autodate | |

Unique indexes on `slug` and `nib_app_id`, and an index on `(owner, created)`. List and view rules: `@request.auth.collectionName = 'users' && @request.auth.suspended = false && (@request.auth.role = 'admin' || owner = @request.auth.id)`; create, update and delete are closed (superusers only). The value rules are `internal/appspec`, shared with the node's renderer; the migration repeats the ranges and patterns as a second check. Quota: an account has at most `app_quota` non-archived apps (admins are exempt).

## App secret names

`pb_migrations/1820000400_app_secret_names.js` creates `app_secret_names`, one row per secret: `app` (relation to `apps`), `name` (`^[A-Z][A-Z0-9_]{0,63}$`), `ssm_version` (the SSM parameter version the app uses), `length` (of the value, 1-4096), `set_via` (`ui` or `mcp`), `updated_by` (relation to `users`), `created`, `updated`. **No value field**: values live only in SSM at `<prefix>/apps/<record id>/<NAME>` ([AWS](aws.md)). A unique index on `(app, name)`. List and view rules: admins, and the app's owner (`app.owner = @request.auth.id`); writes closed. The records are not a public route of the hosted origin: the reader reads them with the `secret.list` and `app.get` operations.

## Releases

`pb_migrations/1820000300_releases.js` creates `releases`, one row per app and executable, written only by the upload route (`internal/dropserver/upload.go`): `app` (relation to `apps`, required), `sha256` (64 lower-case hex characters), `size_bytes` (1 to 90 MiB), `s3_key` (`releases/<sha256>`, where the bytes live in the release store; [AWS](aws.md)), `note` (at most 500 characters), `created_by` (relation to `users`: the account that asked for the upload link) and `created`. There is no PocketBase file field: PocketBase's storage uses its own key layout, and Drop stores by content itself. A unique index on `(app, sha256)` (the same bytes uploaded again reuse the row; another app's row with the same digest shares the stored object) and an index on `(app, created)`. List and view rules as `app_secret_names` (admins, and the app's owner); writes closed. The records are not a public route of the hosted origin: the reader and the CLI read them with `release.list`. It also adds `apps.current_release`.

What the node needs to run a release (M7 and M9) is `sha256`, `s3_key` and `size_bytes` of the app's `current_release`: the desired-state route fills `render.Release{SHA256, S3Key, SizeBytes}` from them, and the agent downloads `s3_key` and refuses bytes whose size or digest differ. An app without a current release is not rendered.

## Nodes

`pb_migrations/1820001000_nodes.js` creates `nodes`, an **auth** collection (never `users`): the hosts that run apps, one agent each ([Agent API](agent-api.md)). Fields: `name` (`^[a-z][a-z0-9-]{1,30}$`, unique: the password identity field), `host_id` (nibrunner's `hostId`), `ec2_instance_id`, `private_ip`, `state` (`active`, `draining`, `disabled`), `desired_seq` (bumped when the node's desired document changes) and `desired_hash` (that document's content hash), and from the agent's reports `capacity`, `allocatable`, `versions` (JSON, at most 2 KiB each), `host_state`, `agent_version`, `accepted_revision`, `last_heartbeat`, `last_report_at`, plus `created`, `updated` and PocketBase's auth fields (`password` at least 32 characters, `tokenKey`; `email` optional and unused). Auth: password only (identity `name`), no OTP, OAuth2 or MFA, tokens for 3600 s, auth rule `state != 'disabled'`. List rule: admins; view rule: admins, and a node its own record (`@request.auth.collectionName = 'nodes' && id = @request.auth.id`, which also governs the realtime subscription `nodes/<id>`); create, update, delete and manage closed. Not a public record route and not an SQL table. Heartbeat fields are written at most every five minutes in steady state.

## Placements

`pb_migrations/1820001100_placements.js` creates `placements`, which node runs an app: `app` (unique: one node per app in v0), `node`, `generation` (up by one per move), `created_by`, `created`, `updated`. List and view: admins, and a node its own placements; writes closed (`placement.set`, `placement.clear`). It also adds `apps.first_placed_at`.

## Node reports

`pb_migrations/1820001200_node_reports.js` creates `node_reports`, the last phase each node reported for each app placed on it: `node`, `app`, `nib_app_id` (unique with `node`), `deployment_id`, `phase` (`pending`, `staging`, `staging_failed`, `secret_failed`, `deploying`, `starting`, `running`, `sleeping`, `unhealthy`, `stopped`, `failed`, `unknown`), `message` (at most 2000), `restart_count`, `last_exit_code` with `has_exit_code`, `converged_at`, optional `last_hook` (event, outcome, time and duration), `observed_at` (when the phase was first reported). A row is rewritten only when something in it changes. List and view: admins, and the app's owner; writes closed. The `status` operation reads it.

## Usage

`pb_migrations/1820001700_usage.js` creates two collections written only by the usage collector (`internal/dropserver/usage.go`, [Hosting](hosting.md#usage-metering)). List and view rules as `node_reports` (admins, and the app's owner); writes closed. Neither is a public record route: the reader and MCP read them with `usage.get`.

- `app_usage_hourly`, one row per app, UTC hour and source: `app`, `hour` (`2026-11-25T14:00:00Z`), `source` (`s3log`), `class_a` (PUT, COPY, POST, LIST and the multipart create, part and complete requests), `class_b` (GET, HEAD and every other request), `deletes`, `bytes_in` (the object size of successful uploads), `bytes_out` (bytes sent), `status_4xx`, `status_5xx`, `denied` (requests S3 refused with 401 or 403: counted only here and in `status_4xx`, never in the classes, bytes or limits, because the key alone does not prove who sent them), `created`, `updated`. A unique index on `(app, hour, source)`.
- `app_usage_daily`, one row per app and inventory day: `app`, `day` (`YYYY-MM-DD`), `current_bytes`, `noncurrent_bytes`, `objects` (current versions that are not delete markers), `versions` (all versions that are not delete markers), `created`, `updated`. A unique index on `(app, day)`; a report replaces its day's rows.

The private tables (no collection API, no SQL reader): `drop_usage_log_objects` (the access-log objects already counted, with when and how many lines; the collector's cursor, pruned after 10 days), `drop_usage_state` (the last inventory manifest read) and `drop_usage_alerts` (one row per app, month, limit and level already raised). They hold object keys, counts and app ids only: never a requester, address or request id from a log.

## Upload links

`pb_migrations/1820000500_upload_links.js` creates `upload_links`, private to the server (every rule closed, never a public route or an SQL table): `app` (relation to `apps`), `token_hash` (the SHA-256 of the 32-character token, hidden; the token itself is shown once and never stored), `created_by` (relation to `users`), `note`, `activate` (deploy on upload), `expires_at` (15 minutes after creation), `used_at` and `release` (set by the one successful upload) and `created`. A unique index on `token_hash` and an index on `expires_at`. Unused links are deleted a day after they expire (hourly); used links stay with their release. See [Operations](ops.md#releases).

## SQL reads

`server.json` lists the SQL-readable tables and columns:

| Table | Columns |
| --- | --- |
| `apps` | `id`, `owner`, `name`, `slug`, `nib_app_id`, `hostname`, `desired_state`, `idle_timeout_ms`, `vcpu`, `memory_mib`, `disk_mib`, `http_port`, `hook_port`, `executable_name`, `health_kind`, `health_path`, `deployment_seq`, `current_release`, `archived`, `archived_at`, `created`, `updated` (not `env` or `args`) |
| `app_secret_names` | `id`, `app`, `name`, `ssm_version`, `length`, `set_via`, `updated_by`, `created`, `updated` |
| `releases` | `id`, `app`, `sha256`, `size_bytes`, `s3_key`, `note`, `created_by`, `created` |
| `events` | `id`, `actor`, `actor_node`, `app`, `subject_user`, `action`, `detail`, `created` |
| `placements` | `id`, `app`, `node`, `generation`, `created_by`, `created`, `updated` |
| `node_reports` | `id`, `node`, `app`, `nib_app_id`, `deployment_id`, `phase`, `message`, `restart_count`, `last_exit_code`, `has_exit_code`, `converged_at`, `observed_at` |
| `app_usage_hourly` | `id`, `app`, `hour`, `source`, `class_a`, `class_b`, `deletes`, `bytes_in`, `bytes_out`, `status_4xx`, `status_5xx`, `denied`, `created`, `updated` |
| `app_usage_daily` | `id`, `app`, `day`, `current_bytes`, `noncurrent_bytes`, `objects`, `versions`, `created`, `updated` |

`POST /api/context/query {"sql": "SELECT ..."}` (`drop sql`) runs one read-only `SELECT` and returns `{columns, rows, truncated}`; `GET /api/context/schema` (`drop schema`) lists the readable tables, columns and search indexes with `"permissionModel": "shared"`. Both require a `users` token. The vendored SQL reader (`internal/pocketcontext/sqlread`) reads the live working database through read-only connections and authorizes every statement with an SQLite authorizer: only listed columns of listed tables, an allowlist of functions, no writes, `PRAGMA`, `ATTACH`, `sqlite_schema`, `dbstat`, `pragma_*` table-valued functions, FTS shadow tables or auth collections. Results are bounded (`maxRows` 500, `maxBytes` 1 MiB, `timeoutMs` 2000); `truncated` says a limit was hit. The column allowlist is the only read restriction and SQL reads whole tables, whoever owns the rows, so these reads stay on the loopback server (the owner's local key) and are never public routes; list only what the owner may read and never secrets.

## Search

`server.json` defines one search index:

```json
"apps": {"collection": "apps", "columns": ["name", "slug"], "snippetColumn": "name"}
```

`POST /api/context/search {"index": "apps", "query": "...", "limit": N, "offset": N, "expectedGeneration": "..."}` (`drop search`) ranks apps with SQLite FTS5 (bm25) over `name` and `slug` and returns hits with `id`, `score` and an `excerpt` of `name`, plus `generation` and `hasMore`. The index is a built index (no `table`): each request reads the indexed columns with fixed SQL in one read transaction of the live database, then searches an in-memory FTS5 index cached by a fingerprint of exactly those rows (`maxIndexBytes` and `cacheBytes` bound it). There is no FTS table in the database and nothing to maintain in migrations or hooks. Every column an index reads must be listed under `tables`. Queries take 1-16 terms, each matched as a whole word, all required, at most 4096 bytes; `limit` is 1-100 and `offset` at most 10,000. The `generation` is that fingerprint: a continuation passes it as `expectedGeneration`, and HTTP 409 (CLI exit 4) means the apps changed, so the client restarts at offset 0. The CLI reads the full records of the hits through SQL.

A built index may also restrict its rows with `"where"`, a predicate over the collection's SQL-readable columns such as `"archived = 0"` (no parameters, comments or quoted identifiers), and may be scoped by a `scope` record whose JSON object values list member IDs, with `"members": "all"` (every member must exist) or `"visible"` (members filtered out by `where` are skipped). Drop uses neither.

## Reader

The reader (`ui/`) asks the server how people sign in (`GET /api/bundle/auth-mode`). On the hosted origin it signs in by email (`ui/src/Login.tsx`, from Crew: the address, then the six-digit code from the email, or the email's link at `#/login`, which asks to confirm); locally it uses the password, a single-use link from `open` or a one-time code from `login-code` (`ui/src/SignIn.tsx`). Hash routes (`ui/src/routes.ts`): `#/apps` (the default) lists the account's apps newest first with its quota (`app.list`; archived apps on request); `#/apps/new` creates one (name, slug and settings); `#/apps/<id>` is an app's page with the tabs Overview (state, hostname, deployment; Start, Stop, Archive, Restore), Settings (`#/apps/<id>/settings`: only changed fields are sent, and a new deployment is reported) and Env & secrets (`#/apps/<id>/env`: plain variables, and write-only secrets in a password field that is cleared after saving, with secrets set through MCP marked and Replace); another account's app shows Not found; `#/admin/users` (admins only; anyone else sees Not found) invites people (showing the sign-in link with Copy), lists and revokes invites and changes accounts' role, quota and suspension through Drop's operations (`ui/src/dropApi.ts`); `#/connect` shows the MCP address and the account's connected apps with Disconnect; `#/invite?email=…` (an invite's sign-in URL) opens the email sign-in with the address filled in; `#/signin?next=/oauth/authorize?…` is where the OAuth consent page sends a browser to sign in and come back. The reader asks `/api/drop/me` for the account's role to show the Users link. Realtime events on `apps` refresh the list and the app page. All app reads and writes go through Drop's operations (`ui/src/dropApi.ts`). The header shows a hosted account's email, never the local account's address. It stores its session in `localStorage` under `drop.reader.auth`.

## Hosted origin

The hosted origin answers only the allowlist in `internal/droppolicy/routes.go` (golden table `internal/droppolicy/testdata/public_routes.json`, test `internal/droppolicy/routes_test.go`) plus the routes core adds; see [Hosting](hosting.md). Record reads of `apps`, `invites` and `events` are public routes and the collection rules decide what each account sees (`app_secret_names` is not: the operations read it), as are `POST /api/drop/op` and `GET /api/drop/me` (the operation framework checks the account and its role); record writes, `users` records, `op_keys`, the SQL and search reads and all local administration are not. The frozen loopback route table `tests/testdata/public_routes.json` (`tests/public_routes.py`) is separate and unaffected.

## Databases from other releases

`pocketcontext-core` (`appserver/legacy.go`, `CheckDatabase`) reads the working copy before PocketBase opens it and refuses the database when its `_migrations` table records a `pb_migrations/*.js` file this binary does not embed: a database of a newer release or of another product (including the template Drop was derived from, whose collection is not `apps`). Drop has no earlier schemas (`Policy.Legacy` is empty). The file is left unchanged and the error says so. Never edit, rename or delete a migration that has shipped; add a new one.

## Adding a collection

To add a collection:

1. **Migration.** Add `pb_migrations/<timestamp>_<name>.js` with a number above the existing ones. Drop's own collections close every write rule (`null`) and change records only through operations ([Operations](ops.md), "Adding an operation"). Never edit a migration that has shipped; add a new one. Give every rule `signedIn` (`@request.auth.id != '' && @request.auth.collectionName = 'users'`) narrowed to the requester's own records (an owner field), or `null` where clients must not act; never an empty rule, which would admit anonymous requests, and never a rule that lets one account read another's records. Add indexes for the reads you expect.
2. **Hooks.** Only when the model needs them: in `pb_hooks/<name>.js` with a `<name>.pb.js` registration, set server-managed fields on create and refuse changes to immutable fields on every update path (`onRecordUpdate`, which covers REST, batch and internal saves). Hooks run in-process: no `$os` calls (`internal/appconfig/hooks_test.go` enforces it). The runtime is PocketBase's goja, not a browser or Node: there is no `URL` constructor and no `fetch`, so validate addresses with a regular expression. A `json` field's `e.record.get(name)` is the raw JSON bytes, not a parsed value (`Array.isArray` is true and `length` counts bytes): read it with `JSON.parse(String(e.record.get(name)))`, treating an empty string or `null` as unset.
3. **SQL exposure.** List the readable columns in `server.json` `tables`, never secrets or auth collections. The list is the whole read policy of SQL: there are no per-requester filters, which is why SQL stays local.
4. **Search.** For full-text search add an index under `search.indexes` with the collection, its text `columns` and a `snippetColumn` (and `where` to leave rows out); every column must also be listed under `tables`. Excerpts come only from the `snippetColumn`, so a match in another column is ranked but its words may not appear in the excerpt. Never list auth or system collections.
5. **Client.** Add commands in `internal/client` (writes through operations with `c.Op`, local reads through `c.Rows`/`c.Query` and `/api/context/search`), with entries in `commandHelp` (`help.go`), the overview (`usage.go`) and the argument spec in `commands.go`; `help_test.go` checks they agree and pins each data command's output keys, so update its expected strings too. `--url` and `--db` are global flags accepted anywhere on the line (`commands.go`), so a record field with one of those names (a bookmark's url) must be positional or use another flag name.
6. **Hosted routes.** A collection whose records the reader reads on the hosted origin goes into `readableCollections` in `internal/droppolicy/routes.go`; regenerate the golden table with `go test ./internal/droppolicy -run TestPublicRouteTable -update-routes` and review the diff.
7. **Reader, skill and tests.** Show the collection in `ui/src`, document the commands in `skills/drop`, and cover the rules (anonymous and invalid-token requests are refused, the account reads and writes), the SQL policy (unlisted columns and writes are refused) and search in `tests/e2e.py`, in `internal/appconfig/sqlite_test.go` if the table needs SQLite features, and in the browser tests.

Then run `make ui build test names identity e2e`.
