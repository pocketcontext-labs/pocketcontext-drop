# Operations

The reader, CLI and MCP tool `drop_write` submit account operations through `POST /api/drop/op` ([MCP](mcp.md)). For a mutation, the server rechecks the account and its permissions in the transaction that writes the change and its event. Release uploads and node reports have dedicated write routes. `GET /api/drop/me` answers the `me` operation.

The wire contract (request, result and error shapes, the registry of operation names, roles and input types) is `internal/dropapi`; the framework and the operations are `internal/dropserver` (`ops.go`, `ops_*.go`).

## Request and answer

```http
POST /api/drop/op
Authorization: <users token>
Content-Type: application/json

{"op": "invite.create", "input": {"email": "friend@example.com"}, "idempotency_key": "f3c1-1"}
```

- `op` names a registered operation; `input` is its object (absent or `null` means `{}`); `idempotency_key` is optional. Unknown fields in the body or the input are refused. The body is at most 256 KiB.
- Success is HTTP 200 with `{"result": …}`, plus `"replayed": true` when an idempotency key replayed an earlier result.
- Failure keeps PocketBase's error shape and adds a stable code: `{"status": 409, "code": "invite_exists", "message": "…", "data": {…}}`. `data.field` names the invalid input field when there is one.

| Status | Codes |
| --- | --- |
| 400 | `invalid_request` (the body is not `{op, input, idempotency_key}`), `unknown_op`, `invalid_input`, `invalid_idempotency_key` |
| 401 | `unauthenticated` (no session; an invalid or expired token is refused before Drop sees it) |
| 403 | `forbidden` (another auth collection, such as a node, or an operation outside the account role), `suspended` |
| 404 | `not_found`: also for a record of another account, so existence never leaks |
| 409 | `account_exists`, `invite_exists`, `invite_closed`, `quota_exceeded`, `slug_taken`, `app_archived`, `app_not_archived`, `deployment_limit`, `idempotency_conflict`, `idempotency_result_unavailable` |
| 413 | `invalid_request` (body too large) |
| 500 | `internal` (details only in the server log, never the input) |
| 502 | `secret_store_failed` (the secret store refused or timed out; nothing changed) |
| 502 | `usage_collect_failed` (the log bucket could not be listed; nothing was counted twice) |
| 503 | `usage_unavailable` (this server does not meter usage), `secrets_unavailable`, `releases_unavailable` (this server has no secret or release store: the local development server without `--fake-aws`) |

The upload route has its own answers ([Releases](#releases)).

### Idempotency keys

A key is 1-128 letters, digits, `.`, `_`, `:` or `-`, and belongs to the calling account. The first successful call stores the key with a hash of the operation and its input, and the result. A retry with the same key and input within 24 hours replays that result without acting again (no second change, no second event). The same key with another operation or input answers 409 `idempotency_conflict`. A failed call stores nothing, so the key stays usable. An operation whose result is shown only once (`release.upload_link`, whose URL carries a credential, and `node.create` and `node.rotate_secret`, which return a node's password) stores no result: its replay answers 409 `idempotency_result_unavailable`. Read operations ignore keys, and so does `secret.set` (`Spec.SecretInput`): not even a hash of a secret value is stored, and setting a secret again is safe (a new version). Keys live in the private `op_keys` collection (hashes only; every rule closed) and are pruned hourly.

## Roles

Every account has a role, `admin` or `user` (decision 4); hosted users manage their own applications. Hosted admins, including the owner, administer accounts, nodes, placement, catalog and metering and can read app metadata. They cannot create, change, upload or deploy applications. Promotion leaves existing app records and deployments in place. The owner (`DROP_OWNER_EMAIL`, or the local account of a local project) is always an admin: it gets the role at creation and again at every start. An invited account takes its invite's role.

## Operations

| Operation | Role | Input | Result |
| --- | --- | --- | --- |
| `me` | user | `{}` | `{id, email, role, admin, owner, app_quota}` |
| `event.list` | user | `{app?, all?, limit?, offset?}` | `{events, has_more}`, newest first |
| `user.list` | admin | `{limit?, offset?}` | `{users, has_more}`, newest first |
| `user.set` | admin | `{user, role?, app_quota?, suspended?}` | `{user}` |
| `invite.create` | admin | `{email, role?, app_quota?, expires_days?}` | `{invite, sign_in_url}` |
| `invite.list` | admin | `{state?, limit?, offset?}` | `{invites, has_more}`, newest first |
| `invite.revoke` | admin | `{invite}` | `{invite}` |
| `app.list` | user | `{all?, archived?, include_archived?, limit?, offset?}` | `{apps, has_more, quota}`, newest first |
| `app.get` | user | `{app}` | `{app, secrets}` |
| `app.create` | user | `{name, slug, kind?, desired_state?, idle_timeout_ms?, vcpu?, memory_mib?, disk_mib?, http_port?, hook_port?, executable_name?, args?, env?, health_kind?, health_path?}` | `{app}` |
| `app.set` | user | `{app, name?, finish_enrollment?, sign_in_email_enabled?, replica_sync_interval?, desired_state?, idle_timeout_ms?, vcpu?, memory_mib?, disk_mib?, http_port?, hook_port?, executable_name?, args?, env?: {set?, unset?}, health_kind?, health_path?}` | `{app, deployment_bumped}` |
| `app.start`, `app.stop` | user | `{app}` | `{app}` |
| `app.archive`, `app.restore` | user | `{app}` | `{app}` |
| `secret.list` | user | `{app}` | `{secrets}` |
| `secret.set` | user | `{app, name, value}` | `{secret, deployment_bumped, deployment_id}` |
| `secret.delete` | user | `{app, name}` | `{deleted, deployment_bumped, deployment_id}` |
| `release.list` | user | `{app, limit?, offset?}` | `{releases, has_more, current_release}`, newest first |
| `release.upload_link` | user | `{app, note?, activate?}` | `{url, path, method, max_bytes, expires_at, activate, example}`, shown once |
| `release.deploy` | user | `{app, release}` | `{app, release, deployment_bumped, deployment_id}` |
| `status` | user | `{app}` | `{app, phase, message, desired_state, deployment_id, reported_deployment_id, hostname, placed, node?, restart_count, last_exit_code, last_hook?, converged_at, observed_at, last_seen}` |
| `node.list` | admin | `{limit?, offset?}` | `{nodes, has_more}`, by name |
| `node.create` | admin | `{name, host_id, ec2_instance_id?, private_ip?}` | `{node, password, identity, collection}`, shown once |
| `node.rotate_secret` | admin | `{node}` | `{node, password, identity, collection}`, shown once |
| `node.set_state` | admin | `{node, state}` | `{node}` |
| `placement.set` | admin | `{app, node}` | `{placement, moved, previous_node?}` |
| `placement.clear` | admin | `{app}` | `{app, cleared_node}` |
| `usage.get` | user | `{app}` | `{app, nib_app_id, month, hourly, month_to_date, stored, limits, limits_override, estimate, estimate_note, exceeded, metered}` |
| `usage.set_limits` | admin | `{app, requests_a?, requests_b?, bytes_out?, stored_bytes?, reset?}` | `{app, override, limits}` |
| `usage.collect` | admin | `{}` | `{objects_listed, objects_processed, objects_failed, lines, malformed, unattributed, ignored, unknown_apps, hours_updated, inventory?, inventory_day?, inventory_apps, filters_created, filters_deleted, filters_failed, alerts, stopped, errors}` |

- **Lists**: `limit` 1-500 (default 100), `offset` 0-10000; `has_more` says more follow.
- **An account** (`user`): `id`, `email`, `role`, `app_quota`, `suspended`, `owner`, `signed_in` (false until its first sign-in; core prunes such accounts after 15 minutes), `invited_by`, `created`.
- **`user.set`** names the account by id or email and needs at least one change. The legacy `app_quota` input is 0-100 and is retained for API compatibility. The allocated volume sizes determine the enforced limit. Nobody changes their own role or suspension, and the owner's role and suspension cannot change (403 `forbidden`). Suspending ends every session of the account at once (its token key changes) and restoration requires fresh email sign-in. A suspended account cannot sign in (the users auth rule is `suspended = false`).
- **An invite**: `id`, `email`, `role`, `app_quota`, `state` (`open`, `used`, `revoked` or `expired`), `invited_by`, `expires_at`, `used_at`, `used_by`, `revoked_at`, `created`.
- **`invite.create`** takes one plain ASCII address (at most 254 characters, lower-cased), role `user` by default, `app_quota` 3 by default (0-100) and `expires_days` 7 by default (1-30). An address with an account answers 409 `account_exists`, one with an open unexpired invite 409 `invite_exists`; an expired open invite is closed and replaced. The result's `sign_in_url` is `https://HOST/#/invite?email=…`: the reader's sign-in page with the address filled in. It carries no credential. **No invite email is sent** (decision 16): the admin shares the link; the invitee's first sign-in sends the ordinary sign-in code email, which creates the account with the invite's role and quota and marks the invite used.
- **`invite.list`** takes `state` `all` (default) or `open` (unused, unrevoked and unexpired).
- **`invite.revoke`** closes an open invite (expired or not); a used or revoked one answers 409 `invite_closed` (suspend the account instead).

## Apps

- **`app`** names an app by record id or slug. Reads (`app.get`, `secret.list`) show an account its own apps and admins every app; writes require a user account that owns the app. Any other app answers 404, so existence never leaks.
- **An app**: `id`, `name`, `slug`, `owner`, `owner_email` (current Drop account address), `kind`, `crew_owner_email` (Crew only), `sign_in_email_enabled`, `nib_app_id` (`d-<id>`), `volume_id` (`d-<id>-vol`), `hostname` (`<slug>.<DROP_APP_DOMAIN>`, empty when the server has no app domain), `desired_state`, `idle_timeout_ms`, `vcpu`, `memory_mib`, `disk_mib`, `http_port`, `hook_port`, `executable_name`, `args`, `env` (plain values only), `health_kind`, `health_path`, `deployment_seq`, `deployment_id` (`<nib_app_id>-dep-<deployment_seq>`), `archived`, `archived_at`, `created`, `updated`.
- **Validation** follows `internal/appspec`, the rules the node's renderer (`internal/render`) applies again before writing nibrunner's `desired.json` (plan 7.3), so the server refuses up front what the node would reject: name 1-80 characters of text; slug `^[a-z][a-z0-9-]{1,30}[a-z0-9]$`, not `drop`, `drop-dev`, `crew-dev`, `www`, `api`, `mail` or `canary`, unique (409 `slug_taken`) and immutable; `desired_state` `running`, `on-request` or `stopped`; `idle_timeout_ms` 60000-86400000; `vcpu` 1-2; `memory_mib` 128-4096; `disk_mib` 256-16384; `http_port` 1-65535; `executable_name` `^[a-z][a-z0-9-]{0,31}$`; at most 64 `args` of at most 4096 bytes each and 4096 in all, no NUL; `health_kind` `tcp` (no path) or `http` (a path `/…` of printable characters, at most 1024; `/` by default); environment and secret names `^[A-Z][A-Z0-9_]{0,63}$`, not `HOME`, `PATH`, `NIBRUN_*` or `DROP_*`, a name either plain or secret, at most 64 together; values at most 4096 bytes, no NUL, and no `${NIBRUN_…}` reference other than `${NIBRUN_HOSTNAME}` and `${NIBRUN_HTTP_PORT}`; the plain environment at most 32 KiB as JSON. `data.field` names the input field.
- `http_port` is the port the executable listens on inside its microVM. Ports below 1024 require a guest image that permits the app user to bind them. Upgrade the Drop node agents before assigning low ports to apps.
- **`app.create`** defaults: `on-request`, 300000 ms, 1 vCPU, 512 MiB, 1024 MiB disk, port 8080, executable `server`, no arguments or environment, a `tcp` health check; deployment 1. A new volume must fit the 10 GiB allowance across all of the caller’s retained volumes. Concurrent creates check the allowance in the same transaction that allocates disk.
- `hook_port` (CLI `--hook-port`) is 0 for no lifecycle listener or a guest port from 1 through 65535 distinct from the app HTTP listener. A change starts a new deployment.
- A fresh Crew enrollment defaults to executable `crew`, TCP health, HTTP 8080 and lifecycle hooks 8081. Drop supplies the hostname, owner email, app ID, region, replica bucket, role session name and asynchronous workload identity. After verifying the owner sign-in and the replica, run `drop app set APP --finish-enrollment` (or `finish_enrollment: true` on `app.set`); this permanently clears the fresh enrollment permission and starts a new deployment.
- **Crew sign-in.** Create a Crew app with `kind: "crew"` (CLI `--kind crew` or select **PocketContext Crew** in the reader). Drop requires a verified hosted account and copies its current email to the immutable `crew_owner_email` property in the creation transaction. Later account changes affect only new apps. Omit `kind` or use `ordinary` for any other app. Earlier apps retain ordinary behavior. The creating account supplies no mail settings. A Crew owner can switch delivery off or back on with `app.set` and the boolean `sign_in_email_enabled` (CLI `--sign-in-email disabled|enabled`).
- **`app.set`** changes the given fields (`args` replaces the list; `env` sets and unsets names, a name in both is refused). The slug is not an input (unknown field, 400). A change to the instance configuration bumps the deployment (below) and answers `deployment_bumped: true`. The disk is refused (409 `disk_locked`) once the app has been placed on a node (even after the placement is cleared: the volume exists there); `disk_locked` in the app says so.
- **Replica sync interval.** A Crew app's Litestream sync interval, `replica_sync_interval` on `app.set` (CLI `--replica-sync-interval`), is a Go duration of whole seconds from `1s` to `300s`; new Crew apps start at `10s` (pre-0.3.0 Crew apps have none and keep the guest's default until it is set). The node hands it to the guest as `DROP_REPLICA_SYNC_INTERVAL` (`10s`). A change starts a new deployment; ordinary apps refuse it (400).
- **`app.start`** sets `desired_state` back to the state the app had before it stopped (`running` or `on-request`, kept as `start_state`); **`app.stop`** sets `stopped`. Neither bumps the deployment.
- **`app.deletions`** lists your deletion receipts newest first (limit and offset), or looks up one by immutable app ID in `app`. Admins may look up an individual receipt for another account. Each receipt gives the original slug, the number of former nodes still to confirm disk removal, and `completed`, which becomes true only after final cleanup releases the slug. Receipts remain available through the operation after cleanup. Use `app.get` with `deletion_inventory:true` to preview the release count and replica mode alongside the app and secret names.
- **`app.delete`** takes the immutable app record ID in `app` and its exact slug in `confirm_slug`. Owners and admins can request it. Acceptance hides the workspace from app lists and lookups immediately and queues permanent removal. Each former node confirms volume removal before the worker erases replica versions, secrets, release records and unshared release objects. The slug becomes available after cleanup succeeds. Retrying the same ID cannot affect a replacement app. Hosting restore pauses pending removals for reconfirmation.
- **`app.archive`** marks an app archived (its node stops it, retaining its volume, secrets and slug and continuing to count its disk against the allowance). Writes to an archived app answer 409 `app_archived`; **`app.restore`** brings it back when the account can deploy and its existing placement has capacity (409 `app_not_archived` for an app that is not archived). `app.list` lists archived apps with `archived: true`. The reader uses `include_archived: true` to include retained legacy workspaces in the main list; with `all: true` (admins only, else 403) it lists every account's. Its `quota` is `{used, limit, unlimited}` for the caller.

### Deployments

nibrunner acts on an instance only when its `deploymentId` changes: a changed configuration under the same id does nothing. So every change to what Drop renders into the instance gives the app the next `deployment_seq` (`internal/dropserver/deployment.go`):

- `app.set`, when one of the instance fields changes value: the run mode (`running` versus `on-request`, which decides the activation block), `idle_timeout_ms`, `vcpu`, `memory_mib`, `http_port`, `hook_port`, `executable_name`, `args`, `env`, `health_kind`, `health_path`, `sign_in_email_enabled`, `replica_sync_seconds`. Values are compared normalized (JSON re-encoded, an empty list or object the same as none), so setting a field to its current value bumps nothing.
- `secret.set` and `secret.delete`, always (a new version or a removed name changes the instance environment).
- `release.deploy`, and an upload link with `activate`, when the current release changes.

The name, `app.start`/`app.stop` (only the desired state, which nibrunner applies to the current deployment), the disk (the volume, fixed once placed) and archive/restore do not bump it. The node's `desired_seq` is a separate counter that goes up on every change to a node's desired document, these included ([Nodes](#nodes-and-placements)). A deployment number past 999999999 answers 409 `deployment_limit`.

A resource increase or restore on a placed app includes the other active placements in the node’s capacity check. Insufficient capacity returns 409 `node_too_small`.

## Secrets

A secret is an environment variable whose value lives only in SSM, at `<prefix>/apps/<app id>/<NAME>` (`awsx.SecretParameter`; the app id without `d-`). The server holds the store as an `awsx.SecretWriter` (put and delete, no read; `TestServerNeverReadsSecrets`), handed over by `serve` (`internal/client/serve_aws.go`, `dropserver.UseServices`). The database keeps `app_secret_names`: name, SSM version, the value's length, `set_via` and who set it ([Data model](data-model.md#app-secret-names)).

- **`secret.set`** checks the name and value (above), stores the value as a new version, records the name and bumps the deployment. The value is never stored, logged, returned or written to an event; a store error is logged with a fixed diagnostic and answers 502 `secret_store_failed` with nothing changed. `set_via` is `mcp` when the MCP tool `drop_write` made the call (it marks its in-process request with `dropserver.WithVia`), else `ui`; the reader marks secrets set through MCP, whose values passed through an assistant's conversation (decision 9).
- **`secret.list`** returns `{name, version, length, set_via, updated_by, updated}` per secret, never a value.
- **`secret.delete`** removes the name from the app, bumps the deployment and queues deletion of all its SSM versions in one database transaction. After commit the server immediately attempts the SSM deletion. A successful operation confirms the change to the app. During a store outage the parameter may remain in SSM until background cleanup succeeds. The server retries at startup and every minute, and completes pending cleanup before reusing the name. An unknown name returns 404.
- `secret.set` uses a 15-second store timeout. If its transaction fails after a successful SSM put, the new version remains unreferenced.
- `TestSecretSentinel` sets a random value through every path (success, refusals, a failing store whose error echoes the value, an MCP-marked call) and checks that it appears in no database table, no operation answer, no `_logs` row and no standard log line.

## Releases

A release is one executable of an app: a static linux/amd64 ELF file of at most 90 MiB (decision 2: under Cloudflare's 100 MB body limit). Its bytes live in the release store (S3, or the `--fake-aws` directory) once per content, at `releases/<sha256>` (`awsx.ReleaseKey`); the database keeps a `releases` row per app and content ([Data model](data-model.md#releases)). The server holds the store as an `awsx.BlobWriter` (put-if-absent and head; `dropserver.Services.Releases`, handed over by `serve`).

- **A release**: `id`, `app`, `sha256`, `size_bytes`, `s3_key` (`releases/<sha256>`), `note`, `created_by`, `created`, `current` (it is the app's `current_release`).
- **`release.upload_link`** (the owner of an app that is not archived) creates a one-time upload link: a 32-character random token whose SHA-256 alone is stored, valid for 15 minutes and for one successful upload. The result is shown once (`OnceResult`: a replayed idempotency key answers 409 `idempotency_result_unavailable`): `url` (`https://HOST/api/drop/upload/<token>`), `path` (the same without the origin), `method` `PUT`, `max_bytes`, `expires_at` (RFC 3339), `activate` and `example` (`curl -fT FILE 'URL'`). `note` (at most 500 characters, one line) is kept with the release; `activate: true` deploys it once uploaded. Its event holds the link id, expiry and `activate`, never the token. A server without a release store answers 503 `releases_unavailable`.
- **`release.list`** shows an app's releases newest first (to its owner and admins) with `current_release`.
- **`release.deploy`** makes one of the app's releases (by id; another app's release is 404) its current release and bumps the deployment; the current release again changes nothing (`deployment_bumped: false`).

### The upload route

`PUT /api/drop/upload/<token>` (`internal/dropserver/upload.go`) is anonymous: the token is the credential, so the link can be used by `curl -fT FILE URL` from any machine, by the reader (with a progress bar) and by `drop release upload`. It is public only for a token of exactly 32 letters and digits (`droppolicy.PublicRoute`; anything else is 404 before Drop sees it).

1. Each client address gets 6 attempts at once, then one every 10 seconds, and the server runs at most 4 uploads at a time: 429 `rate_limited` with `Retry-After`.
2. The link must exist, be unused and unexpired, belong to an app that is not archived and to an account that is not suspended: otherwise one 404 `upload_link_invalid` for every case (409 `app_archived` for an archived app). These checks happen before the body is read, so a client that sends `Expect: 100-continue` (curl does) sends nothing to a dead link.
3. The body streams to a private temporary file while it is hashed (SHA-256), at most `max_bytes` (90 MiB, or `DROP_RELEASE_MAX_BYTES`): 413 `release_too_large` beyond, from `Content-Length` or while reading. Its first 64 bytes must be a 64-bit little-endian ELF executable or position-independent executable for x86-64 with the System V or Linux ABI (`dropapi.ReleaseHeaderProblem`): 400 `invalid_release` otherwise (also for an empty or interrupted body), checked before the rest is read. Before storage, the server checks the program headers, file-backed executable entry point, and absence of an interpreter or shared-library dependencies.
4. The file goes to the release store with `PutIfAbsent(releases/<sha256>, size, sha256)`: an object already there holds the same bytes. A store failure answers 502 `release_store_failed`.
5. One transaction creates the release (or reuses the app's release with the same digest), marks the link used, deploys the release when the link says `activate` (a deployment bump unless it was already current), and writes one `release.upload` event as the link's account.

The answer is 200 `{release, created, deployed, deployment_id}`: `created` is false when the app already had these bytes, `deployed` true when the upload changed the current release. Only a successful upload uses the link: a refused body leaves it usable until it expires, and two uploads at once to one link answer 409 `upload_in_progress` to the second. Unused links are deleted a day after they expire.

## Nodes and placements

Nodes are the hosts that run apps; each runs one agent that signs in as its node and uses only the agent routes ([Agent API](agent-api.md)). These operations are admin-only, except `status`.

- **A node**: `id`, `name` (2-31 lowercase letters, digits and hyphens: the agent's sign-in identity), `host_id` (nibrunner's `hostId`), `ec2_instance_id`, `private_ip` (for people), `state` (`active`, `draining` or `disabled`), `desired_seq`, `online` (a report in the last 90 seconds), `last_heartbeat`, `last_report_at`, `host_state`, `agent_version`, `accepted_revision`, `capacity`, `allocatable`, `versions` (nibrunner's, from the reports) and `apps` (how many are placed on it). `node` names one by id or name.
- **`node.create`** generates a 40-character password and returns it once (`OnceResult`; never in an event). A taken name or host id answers 409 `node_exists`. **`node.rotate_secret`** replaces it the same way; the old password and the agent's tokens stop working. **`node.set_state`**: `draining` keeps the node's apps and serves its agent but takes no new placements; `disabled` ends its tokens and refuses its sign-in.
- **`placement.set`** places any account's app (by id or slug; not archived, else 409 `app_archived`) on an active node (409 `node_unavailable` otherwise) that can allocate the app's vCPUs and memory together with the other apps placed there that are not archived (stopped and on-request apps count: they start without another placement), per nibrunner's last reported `allocatable` (409 `node_too_small`, with the totals; unknown allocatable skips the check). One node per app: placing it on another node moves it (`moved: true`, `generation` goes up); the old node stops it as a tombstone and keeps its data (volumes do not move in v0). The first placement fixes the app's disk. **`placement.clear`** removes the placement (404 when there is none); the node stops the app and keeps its data. Both delete the app's node reports from nodes it left.
- **`status`** (an account's own app; admins any) gives the runtime phase: `archived`, `no_release`, `unplaced`, `pending` (placed, not reported yet), the node's reported phase (`staging`, `staging_failed`, `secret_failed`, `deploying`, `starting`, `running`, `sleeping`, `unhealthy`, `stopped`, `failed`), `deploying` while the node still reports an older deployment, and `unknown` when the node has not reported for 90 seconds. `node` (the node's name) is for admins only.

### Desired sequence

Every mutating operation, and an upload link with `activate`, rebuilds each node's desired document in its transaction and bumps the node's `desired_seq` where the content changed (`internal/dropserver/seq.go`): app settings and the desired state, archive and restore, secrets, the current release, placements (both nodes when an app moves) and, from the agent's reports, a new allocatable. A rename, an upload without `activate` or an invite changes no document and bumps nothing. The agent subscribes to its own `nodes` record and pulls when `desired_seq` goes up.

## Usage

S3 usage is metered from the replica bucket's server access logs and daily inventory by the usage collector ([Hosting](hosting.md#usage-metering), [Data model](data-model.md#usage)). Every guest's STS session may touch only keys under `<app id>/`, so the first segment of a request's key (or of a listing's `prefix`) names the app; requests S3 refused (401, 403) count only as denied.

- **`usage.get`** (an account's own apps; admins any; another account's app is 404): `hourly` is the last 24 hours by UTC hour, oldest first, hours without requests left out; `month_to_date` sums the calendar month (UTC); `stored` is the latest inventory row (or null); `limits` lists `requests_a` (Class A plus DELETE), `requests_b` (Class B), `bytes_out` and `stored` (current plus noncurrent bytes of an inventory at most a week old) with `value`, `soft`, `hard` and `level` (`ok`, `soft`, `hard`); `estimate` is the month's cost at list prices (Class A $0.005 per 1,000, Class B $0.0004 per 1,000, egress $0.09 per GB, storage $0.023 per GB-month; DELETE free), an estimate and never a bill; `exceeded` lists the month's `app.usage_exceeded` events; `metered` says whether the server runs the collector.
- **Limits ("Standard").** Soft limits per app and calendar month: 100,000 Class A and DELETE requests, 1,000,000 Class B requests, 2 GB sent, 1 GB stored (GB = 2^30 bytes). Hard limits are ten times the soft ones. Past a soft limit the collector writes one `app.usage_exceeded` event (level `soft`) per limit and month; past a hard limit one more (level `hard`), and it stops the app through the stop path (`desired_state` `stopped`, the node's `desired_seq` bumped in the same transaction) once: the owner may start it again, and that limit does not stop it again that month. Archived and already stopped apps are not stopped again.
- **`usage.set_limits`** (admins, any app) sets an app's soft limits (1 to 2^50 each); absent fields keep their value, `reset: true` returns every limit to the default first. It writes one event with `{before, after, reset}`. It does not reopen alerts already raised this month. Not offered through MCP.
- **`usage.collect`** (admins) runs the collector once and returns its counts. It is a standalone operation (`Spec.Standalone`): it reads S3 outside the framework's transaction, commits each log object with its cursor row on its own, ignores idempotency keys and writes one `usage.collect` event with its counts. 503 `usage_unavailable` without metering; 502 `usage_collect_failed` when the log bucket cannot be listed. Not offered through MCP.
- **Request-metrics filters.** With metering on and `DROP_CREW_REPLICA_BUCKET` set, `app.create` of a Crew app and `app.restore` of an archived one create a CloudWatch request-metrics configuration on the replica bucket (Id the app id, filter prefix `<app id>/`), and `app.archive` removes it. The call runs after the operation commits, best effort: a failure never fails the operation. Each attempt writes an `app.metrics_filter` event `{action, id, prefix, ok, error?}` (`error` an AWS error code, never a message) with the caller as actor. Every collector run reconciles the filters (creating missing ones, removing archived apps'; filters whose id is not a Drop app are left alone) and records each change as an `app.metrics_filter` event with `reconciled: true` and no actor.

## Events

Each successful mutating operation writes one `events` row in its transaction: `actor` (the caller), `action` (the operation name unless the operation says otherwise), `app` and `subject_user` when it acts on one, and `detail`, a JSON object of at most 8 KiB that never holds a secret value (names, lengths and before/after values of plain fields only). App events name what changed: `app.create` `{name, slug}`; `app.set` `{changed, env_set?, env_unset?, deployment_bumped, deployment_seq}` (environment names, never values); `app.start`/`app.stop` `{desired_state}`; `app.archive`/`app.restore` `{slug}`; `secret.set` `{name, version, set_via, deployment_seq}`; `secret.delete` `{name, deployment_seq}`; `release.upload_link` `{link, expires_at, activate}` (never the token); `release.upload` (written by the upload route, actor the link's account) `{release, sha256, size_bytes, link, created, activate, deployed, deployment_seq}`; `release.deploy` `{release, sha256, deployment_bumped, deployment_seq}`; `node.create` `{node, name, host_id}` and `node.rotate_secret` `{node, name}` (never the password); `node.set_state` `{node, name, state, previous_state}`; `placement.set` `{node, generation, moved, previous_node?}` and `placement.clear` `{node}` (node ids). The usage collector writes `app.usage_exceeded` `{metric, level, month, value, limit, soft, hard, stopped, message}` and reconciled `app.metrics_filter` events with no actor; operations that create, archive or restore a Crew app write `app.metrics_filter` after their own event; `usage.set_limits` writes `{before, after, reset}` and `usage.collect` its counts. The agent's report route writes `app.phase` `{phase, previous_phase?, deployment_id}` with `actor_node` (the node) instead of `actor` when an app's phase changes (not between `running`, `sleeping` and `starting`). Read operations write nothing. Collection rules show admins every event and other accounts the events of their own apps.

**`event.list`** reads them the same way: without input, the events of the caller's own apps; with `app` (id or slug), that app's events (admins may name any app; another account's app is 404); with `all: true` (admins only, else 403; not with `app`), every event, account events included. Each entry is `{id, action, actor, actor_email, app, subject_user, detail, created}`; `actor_email` is filled for the caller's own actions and, for admins, everyone's.

## Adding an operation

1. **Contract.** Add a `Spec` to `specs` in `internal/dropapi/ops.go` (`Name` like `app.create`, `Role`, a one-line `Summary` that MCP shows, `Read` for operations that change nothing, `Input: &YourInput{}`, `OnceResult` for results shown only once, `SecretInput` for an input holding a secret value, `MCP` to offer a mutating operation through the MCP tool `drop_write`, whose op enum and table are generated from these specs; reads reach MCP through `drop_read`'s `what` table in `internal/client/drop_mcp.go`) and its input and result types beside it (JSON names in `snake_case`; pointer fields for optional changes).
2. **Handler.** In `internal/dropserver/ops_<area>.go`, register it in an `init` function:

   ```go
   func init() { register("app.create", appCreate) }

   func appCreate(c *Ctx, in *dropapi.AppCreateInput) (any, error) {
       // c.App is the transaction: query and save only through it.
       // c.Auth is the caller's users record; c.Admin and c.Owner its standing.
       app, err := c.LoadOwned("apps", in.App, "owner", "App") // 404 unless the caller owns it
       ...
       c.Event(Event{App: app.Id, Detail: map[string]any{"slug": slug}})
       return dropapi.AppResult{App: out}, nil
   }
   ```

   `register` panics at start when the name has no spec or the input type differs, and `TestEveryOperationHasAHandler` fails when a spec has no handler.
3. **Errors.** Return `Invalid(field, message)` (400), `NotFound(what)` (404, also for other accounts' records), `Forbidden(message)` (403) or `Conflict(code, message)` (409, with a new `Code…` constant in `dropapi`). Any other error is a 500 whose text goes only to the log. Returning an error rolls the transaction back.
4. **Ownership.** `c.LoadOwned(collection, id, ownerField, what)` returns the record only to its owner; `c.LoadVisible` also to admins. Both answer 404 otherwise.
5. **Events outside operations.** Routes that are not operations (uploads, the agent) call `dropserver.WriteEvent(app, dropserver.EventRecord{…})` themselves.
6. **Clients.** Add the name to `OPS` in `ui/src/dropApi.ts` (`TestReaderKnowsEveryOperation` compares the lists), a CLI command if people need one (`internal/client`, building the request with `dropapi.Build`), and the operation to this page, [MCP](mcp.md) and the skill. A write for assistants sets `MCP: true` (`TestMCPWriteOpsFollowTicket` lists the operations decision 11 expects there); a new read for assistants gets a row in `dropReads` (`internal/client/drop_mcp.go`), never a new tool.
7. **Tests.** Cover the role (a user calling an admin operation gets 403), ownership (another account's record is 404), validation, the event and isolation with the `internal/droptest` helpers (`h.User`, `h.Invite`, `h.Op`, `h.MustOp`).

## Beta release admission and deployment

Invited users may submit static Linux amd64 executables up to 90 MiB. Drop inspects the bytes in an isolated NibRunner microVM and caches the outcome by SHA-256. An administrator can revoke a specific digest.

| Operation | Input | Result |
|---|---|---|
| `beta.approve` (admin) | `{sha256}` | Re-enables a revoked digest. |
| `beta.claim` (admin) | `{sha256}` | Returns `attempt` and `expires_at` for a five-minute inspection lease, or `{cached:true,state}` after completed inspection. An overlapping claim returns `probe_busy`. |
| `beta.complete` (admin) | `{sha256,attempt,outcome,manifest?}` | Records `success` with a validated manifest, `invalid` for a definitively incompatible binary, or `retry` after a temporary failure. Expiration or revocation fences earlier attempts. |
| `beta.revoke` (admin) | `{sha256}` | Revokes the admission and stops its Beta apps in the same transaction. Reapproval does not automatically restart them. |
| `beta.inspect` | `{sha256}` | Returns product name, version, `multipliers` and `suggested_multiplier`. A completed incompatible probe yields a generic profile. A cache miss returns `probe_pending`. |
| `beta.probe_upload` | `{sha256}` | Returns a one-time PUT path for a digest-bound probe upload. The server revalidates the ELF and digest before starting inspection. |
| `beta.prepare` | `{sha256,multiplier,http_port?,name?}` | Creates one quota-counted managed app. Reuse the same operation idempotency key when retrying a request with an uncertain result. |
| `beta.deploy` | `{app,release}` | Validates the app's recorded digest, revocation status, inspection, size and quota. Places the app on an eligible node and starts the selected release in one transaction. |

Crew is the first supported runtime. Its manifest requires HTTP 8080, TCP health checks, hooks on 8081 for suspend, resume and terminate, persistent volume and replica storage, a verified owner address and managed sign-in email. Each Beta size unit allocates 256 MiB, one quarter vCPU and 2 GiB. Crew offers sizes 1× through 8×. Drop suggests the smallest size that satisfies all three resource values in the manifest, up to 8×. The released Crew profile suggests 4×. Owners can choose a smaller or larger allocation. A fractional size reserves the next whole number of guest vCPUs and applies an exact quarter-core host CPU quota. Placement for those sizes requires a node reporting support for quarter-core quotas. Normal users cannot override a managed app's runtime settings.

After preparation, request an ordinary `release.upload_link` with `activate:false`. The first Beta upload and every manifest-managed upload must hash to the app's pinned digest, checked before object storage and again when committing its release. Use `beta.deploy` after the upload returns its release ID. If capacity is unavailable, reuse the existing app and release. A retry never moves an already placed volume. Retrying a failed reported deployment of the current release advances its deployment number once. For the current phase, poll `status`; the optional `verify_url:true` asks Drop to check the advertised HTTPS endpoint and report `url_ready`.

The reader imports ZIP and tar.gz archives of up to 90 MiB, verifies a supplied SHA-256, and lists compatible static Linux amd64 members. It accepts up to 4,096 entries and 384 MiB expanded. Owners select a program, an argument array, non-secret environment settings, packaged assets and an optional seed directory. The reader packages these into a single executable for the usual inspection and upload path. The guest verifies the member digest and ELF, runs from the extracted release root and points `./data` to `/app/data`. A seed directory is copied under its own name on the first start with empty application storage. A new generic release can replace a deployed generic app’s executable and assets on the existing node and volume. The generic runtime keeps its configured port and resource allocation.

For imported archives Drop checks the assigned public HTTPS origin after the process starts. PocketBase serves its health response at `/api/health`; other imported apps are checked at `/`. A timeout, missing route or upstream error keeps the HTTP check pending. The deployment page keeps the verified release and startup stages visible.

The status of a generic app can include a private, expiring `first_run` action for the current deployment. Its owner can open the PocketBase installer at the assigned HTTPS hostname or copy the ZeroClaw pairing code into the dashboard. Only the matching app owner can read the action and startup output.

The first catalog admission or a configured Beta node list makes direct `app.create` an admin-only workflow. Existing operator-managed apps keep their release operations. Catalog and probe records stay out of account queries and the public route table.

Install `deploy/drop-ax41/drop-manifest-probe.socket` and `drop-manifest-probe@.service` in the systemd unit directory on the NibRunner host serving Drop. Set the socket's owner to the Drop service account. Run `systemctl daemon-reload` and `systemctl enable --now drop-manifest-probe.socket`. Drop passes only validated bytes and their digest over the private socket. The worker calls `nibrunnerd probe-manifest` with the host's `/etc/nibrunner/config.toml`. NibRunner uses an isolated, time-limited microVM for `--drop-manifest`. The guest receives no account credentials, network or app volume. Infrastructure failures are retriable.

#### Public manifest interface

When invoked with `--drop-manifest`, print one JSON object of at most 16 KiB to stdout and exit. Example:

```json
{"schema":"drop.manifest/v1","app":"sample","version":"1.0","http_port":8080,"hook_port":0,"health":"tcp","hooks":[],"requires":["persistent_volume"],"optional":[],"minimum":{"memory_mib":256,"cpu_quarters":1,"disk_mib":2048}}
```

Use a lowercase app slug (3–32 characters), an alphanumeric release version that may include periods, hyphens and plus signs, a TCP port from 1 through 65535, and positive resource minima. Declare all three lifecycle hooks (`suspend`, `resume`, `terminate`) when a distinct hook port is supplied. Runtime features are `persistent_volume`, `verified_owner_email`, `sign_in_email` and `s3_replica`. Each service can be declared independently. The host uses the verified account address to deliver sign-in email. Only the verified-owner-email declaration exposes that address in the guest environment. Required S3 blocks deployment until the account has credits. Declare optional replication when the binary can start without S3. Drop grants platform S3 through the metered host proxy. The local volume is an unguaranteed cache.

Executables without a compatible manifest use the advanced form, with an explicit port and size, `PORT` and NibRunner's guest environment. The owner can read up to 1 KiB of the current deployment's stdout and stderr in the app view. They receive no email or platform S3. A completed negative probe is cached. Rejected ELF headers and oversized files are refused before inspection.

For a pending Beta invitation, both the six-digit code and emailed-link redemption requests carry `X-Drop-Accept-Terms` with the current immutable version after the user explicitly accepts the terms. Sending an email address or opening a link creates no receipt. A valid credential without the current acceptance receives `428 terms_required`; unpublished or incomplete operator legal configuration receives `409 beta_activation_closed`. Neither response issues a session. The user may request another code after reviewing the documents. Managed app results include `beta_sha256`, allowing a returning owner to resume a prepared upload with the original bytes and selected size.

### Verified waitlist

For a deploy-time or login access lookup, POST `{email,check_access:true}` to `/api/drop/waitlist/request`. This deliberately discloses whether the address has access and does not enqueue email or create an entry. The ordinary login adds `login_flow:true` so the response also indicates whether activation requires Beta consent. Drop checks only on submit, applies the existing source-address limit and returns no account IDs, invitation records or waitlist membership. Returning activated accounts receive the sign-in code immediately. New Beta invitations require acceptance before requesting the code. An unrecognized address stays on the same screen with the waitlist terms and a Join the waitlist button. The standalone `#/waitlist` page remains available. For enrollment, the hosted reader posts an email and the accepted waitlist document version (`2026-11-26-waitlist`) to `/api/drop/waitlist/request`. A valid request returns 202 even when the address is already enrolled or delivery is limited. The waitlist branch requests only a waitlist verification code. Existing accounts and addresses with an open invitation use sign-in. Other addresses receive the waitlist verification email with a six-digit code valid for ten minutes. A returning waitlist member can reconfirm without changing the original verification date. POST the email and code to `/api/drop/waitlist/verify` to join. Verification creates a queue entry. The account and hosting terms are handled separately at invited sign-in. The email also includes a withdrawal link. POST its token to `/api/drop/waitlist/leave` to remove the entry and close an outstanding invitation. An activated account retains its hosting access.

Admins use `waitlist.list` with an optional case-insensitive email substring (`search`), `limit` and `offset`. Only verified addresses appear, ordered by verification time, oldest first. `waitlist.invite`, `waitlist.resend` and `waitlist.revoke` take an entry `id`. Inviting records a regular user invitation and queues its welcome email in the same transaction. A repeated invite reuses the existing invitation. Resend queues another welcome for an open invitation after a one-minute cooldown. Use `user.set` to suspend or restore an active account. Restoring queues an email to the verified address.

Each source address gets a burst of 20 public waitlist operations, replenished by one every five seconds. The hosted proxy supplies the source after checking its configured trusted peer. A mailbox can request a code once per minute and five times in 24 hours. The service caps code issuance at 100 per hour. Five wrong guesses exhaust a code. The hourly cleanup removes unverified requests older than 24 hours. Codes and withdrawal links are derived from a private per-project key. Delivery jobs persist across restarts. SMTP failures retry with increasing delays for up to eight attempts; an administrator can resend an open invitation after a failed delivery. A crash after SMTP accepts a message but before Drop records delivery can produce a duplicate email.

A hosted restore drops unverified waitlist requests and cancels queued delivery, including jobs leased before the backup. Verified entries retain their withdrawal links. An administrator may resend an invitation that remains open after reviewing the restored records. The admin list includes invitation, expiry and activation timestamps when available.

## Private Beta credits

The Credits page and these operations use integer microcredits (1,000,000 per credit). Each account starts with a shared 3,600-second awake buffer. Each successful purchase resets it to 3,600 seconds once, including when the balance was already positive. Adjustments and purchase retries preserve it. Paid compute consumes spendable credits first. When an in-flight S3 request has reserved a positive balance, Drop retains unsettled compute seconds and bills them when funds become available. These seconds draw from the buffer only after actual depletion, and only for time since the last purchase. Workspaces can pause if outstanding compute would exhaust the remaining buffer while funding is unresolved. At zero, each running workspace uses one buffer second per awake second regardless of size, so two awake workspaces use two seconds together. Sleeping consumes zero compute credits. At size N×, the hourly rate is N credits.

| Operation | Role | Input and result |
| --- | --- | --- |
| `credits.get` | User or admin | Own balance, seconds consumed, burn per app, S3 and compute charges, and the last 100 ledger entries. An administrator may select `account`. Supply `before` with the oldest entry ID for the next page. |
| `credits.accounts` | Admin | Account email, balance and total simulated purchases, paginated with `limit` and `offset`. Optional `query` searches email and account ID. |
| `credits.buy` | User | Empty input, required request idempotency key. Adds one configured simulated pack and returns the balance. A purchase above the cap returns `credit_cap`. |
| `credits.adjust` | Admin | `account` and signed `microcredits`, required request idempotency key. The transaction records the actor and adjustment and checks the cap and available funds. |
| `credits.configure` | Admin | Supply integer `pack`, `cap`, `starting` in whole credits, `euro_micros_per_credit`, `eur_per_usd_ppm` and `markup_ppm`. The initial values are 500, 10000, 0, 10000, 1000000 and 1250000 respectively. EUR/USD is a configurable simulation rate. |

Retained disk includes every app volume owned by the account, including archived volumes. The app listing's `quota.used` and `quota.limit` are MiB. A volume increase and a new app share the same 10 GiB check. At buffer exhaustion Drop places a billing stop on running workspaces and retains their volumes. A top-up resumes these workspaces and restores any app archived by the pre-upgrade trial. Funded and local-only transitions reuse the existing release and volume. Manually archived apps stay archived.

### Linked release imports

A public GET to `/deploy` forwards the supplied query to the reader. The link can
specify `name`, an HTTPS `binary` URL (including ZIP or tar.gz), an archive
`sha256`, an HTTP `port`, a JSON array of literal arguments in `arg`, and
`minimal=true`. Opening the link does not reserve a slug, download code or spend
an invitation. The signed-in deployment form reviews the release and settings.

The reader uses `beta.import_url` to download the selected release once.
It accepts a public IPv4 HTTPS origin on port 443, follows up to five HTTPS
redirects and checks the optional SHA-256 against the exact downloaded bytes.
The response contains the digest and base64 content. The control plane limits
downloads to 90 MiB, two concurrent imports, and two minutes per download.

## Official Crew and Atlas workspaces

The administrator stages and checks both binaries before switching an existing
installation. Use **Releases** in the hosted admin reader. All transitions also
use the authenticated operation endpoint.

1. Call `catalog.upload` with `product` (`crew` or `atlas`) and `version`.
   PUT the Linux executable to its one-time path. Drop computes its digest,
   checks the ELF, runs the existing isolated manifest probe and checks the
   product, version, hooks, verified sign-in and optional replica capabilities.
2. Exercise the actual candidate on disposable data. For a first release,
   create records and attachments, restart it and verify their contents.
   For an upgrade, seed the exact baseline, run the candidate against the
   same volume and compare complete record and attachment digests. Retain
   the test inputs, outputs and binary hashes at an immutable evidence URL.
3. Import the JSON receipt under **Data rehearsal receipt**, or call
   `catalog.rehearse` with these fields:

   | Field | Value |
   | --- | --- |
   | `sha256`, `baseline_sha256` | Candidate and baseline executable SHA-256. Use an empty baseline for the initial release. |
   | `evidence_url`, `evidence_sha256` | Durable HTTPS artifact URL and its SHA-256. |
   | `records`, `attachments` | Positive fixture counts. |
   | `before_records_sha256`, `after_records_sha256` | Canonical record content digests before and after migration. |
   | `before_attachments_sha256`, `after_attachments_sha256` | Canonical attachment-content digests before and after migration. |

   The receipt records the operator’s attestation. Drop checks the hashes and
   exact version pair and retains the immutable receipt; the operator runs
   the rehearsal and retains its evidence. Keep secrets and customer data
   out of these artifacts.
4. `catalog.publish {sha256}` requires a matching receipt for the currently
   published baseline. Workspaces several versions behind need a receipt
   for their own installed baseline before upgrading. After publishing both
   products, call `catalog.activate {}` to switch hosted creation to the
   official catalog. Existing placements, volumes and addresses remain in
   place. Old customer upload links expire at cutover.

Customers use `catalog.list`, `workspace.create {product,name,multiplier}`,
`workspace.resize {app,multiplier}` and `workspace.upgrade {app,sha256}`.
Both products support 1×–8× compute (4× suggested) within the chosen release’s manifest minimum. An older release that requires 4× must be upgraded before selecting 1×. A target release requiring more CPU or memory needs a resize before installation. Each unit provides
256 MiB memory and a quarter vCPU. New workspaces allocate at least 2 GiB
persistent disk. Resizing compute keeps that disk fixed. Publication affects
new workspaces immediately. Existing workspaces install only the specific
version their owner accepts. A matching healthy node report confirms the
installed version. Failed deployments can retry the same target; a failed
migration never selects an older executable to open the migrated volume.

Hosted admins use `user.directory` to page a combined directory of signed-in
accounts, verified waitlist entries and invitations. Search accepts an email
substring or account ID. Filters cover account status, role, presence of
workspaces, and positive credit balance. Waiting is a directory status.
