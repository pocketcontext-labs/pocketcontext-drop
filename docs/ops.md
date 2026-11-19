# Operations

Every write in Drop goes through one route, `POST /api/drop/op`, from the reader, the CLI and (later) the MCP tool `drop_write`. The server checks the caller's account, its role and the input in one place, runs the operation in one transaction and records one row in `events`. `GET /api/drop/me` answers the `me` operation.

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
| 403 | `forbidden` (another auth collection, such as a node, or an admin operation called by a user), `suspended` |
| 404 | `not_found`: also for a record of another account, so existence never leaks |
| 409 | `account_exists`, `invite_exists`, `invite_closed`, `quota_exceeded`, `slug_taken`, `app_archived`, `app_not_archived`, `deployment_limit`, `idempotency_conflict`, `idempotency_result_unavailable` |
| 413 | `invalid_request` (body too large) |
| 500 | `internal` (details only in the server log, never the input) |
| 502 | `secret_store_failed` (the secret store refused or timed out; nothing changed) |
| 503 | `secrets_unavailable` (this server has no secret store: the local development server without `--fake-aws`) |

### Idempotency keys

A key is 1-128 letters, digits, `.`, `_`, `:` or `-`, and belongs to the calling account. The first successful call stores the key with a hash of the operation and its input, and the result. A retry with the same key and input within 24 hours replays that result without acting again (no second change, no second event). The same key with another operation or input answers 409 `idempotency_conflict`. A failed call stores nothing, so the key stays usable. An operation whose result is shown only once (a generated credential, from M7) stores no result: its replay answers 409 `idempotency_result_unavailable`. Read operations ignore keys, and so does `secret.set` (`Spec.SecretInput`): not even a hash of a secret value is stored, and setting a secret again is safe (a new version). Keys live in the private `op_keys` collection (hashes only; every rule closed) and are pruned hourly.

## Roles

Every account has a role, `admin` or `user` (decision 4); admins may call every operation. The owner (`DROP_OWNER_EMAIL`, or the local account of a local project) is always an admin: it gets the role at creation and again at every start. An invited account takes its invite's role.

## Operations

| Operation | Role | Input | Result |
| --- | --- | --- | --- |
| `me` | user | `{}` | `{id, email, role, admin, owner, app_quota}` |
| `user.list` | admin | `{limit?, offset?}` | `{users, has_more}`, newest first |
| `user.set` | admin | `{user, role?, app_quota?, suspended?}` | `{user}` |
| `invite.create` | admin | `{email, role?, app_quota?, expires_days?}` | `{invite, sign_in_url}` |
| `invite.list` | admin | `{state?, limit?, offset?}` | `{invites, has_more}`, newest first |
| `invite.revoke` | admin | `{invite}` | `{invite}` |
| `app.list` | user | `{all?, archived?, limit?, offset?}` | `{apps, has_more, quota}`, newest first |
| `app.get` | user | `{app}` | `{app, secrets}` |
| `app.create` | user | `{name, slug, desired_state?, idle_timeout_ms?, vcpu?, memory_mib?, disk_mib?, http_port?, executable_name?, args?, env?, health_kind?, health_path?}` | `{app}` |
| `app.set` | user | `{app, name?, desired_state?, idle_timeout_ms?, vcpu?, memory_mib?, disk_mib?, http_port?, executable_name?, args?, env?: {set?, unset?}, health_kind?, health_path?}` | `{app, deployment_bumped}` |
| `app.start`, `app.stop` | user | `{app}` | `{app}` |
| `app.archive`, `app.restore` | user | `{app}` | `{app}` |
| `secret.list` | user | `{app}` | `{secrets}` |
| `secret.set` | user | `{app, name, value}` | `{secret, deployment_bumped, deployment_id}` |
| `secret.delete` | user | `{app, name}` | `{deleted, deployment_bumped, deployment_id}` |

- **Lists**: `limit` 1-500 (default 100), `offset` 0-10000; `has_more` says more follow.
- **An account** (`user`): `id`, `email`, `role`, `app_quota`, `suspended`, `owner`, `signed_in` (false until its first sign-in; core prunes such accounts after 15 minutes), `invited_by`, `created`.
- **`user.set`** names the account by id or email and needs at least one change. `app_quota` is 0-100. Nobody changes their own role or suspension, and the owner's role and suspension cannot change (403 `forbidden`). Suspending ends every session of the account at once (its token key changes) and a suspended account cannot sign in (the users auth rule is `suspended = false`).
- **An invite**: `id`, `email`, `role`, `app_quota`, `state` (`open`, `used`, `revoked` or `expired`), `invited_by`, `expires_at`, `used_at`, `used_by`, `revoked_at`, `created`.
- **`invite.create`** takes one plain ASCII address (at most 254 characters, lower-cased), role `user` by default, `app_quota` 3 by default (0-100) and `expires_days` 7 by default (1-30). An address with an account answers 409 `account_exists`, one with an open unexpired invite 409 `invite_exists`; an expired open invite is closed and replaced. The result's `sign_in_url` is `https://HOST/#/invite?email=…`: the reader's sign-in page with the address filled in. It carries no credential. **No invite email is sent** (decision 16): the admin shares the link; the invitee's first sign-in sends the ordinary sign-in code email, which creates the account with the invite's role and quota and marks the invite used.
- **`invite.list`** takes `state` `all` (default) or `open` (unused, unrevoked and unexpired).
- **`invite.revoke`** closes an open invite (expired or not); a used or revoked one answers 409 `invite_closed` (suspend the account instead).

## Apps

- **`app`** names an app by record id or slug. Reads (`app.get`, `secret.list`) show an account its own apps and admins every app; writes only the owner. Any other app answers 404, so existence never leaks.
- **An app**: `id`, `name`, `slug`, `owner`, `owner_email`, `nib_app_id` (`d-<id>`), `volume_id` (`d-<id>-vol`), `hostname` (`<slug>.<DROP_APP_DOMAIN>`, empty when the server has no app domain), `desired_state`, `idle_timeout_ms`, `vcpu`, `memory_mib`, `disk_mib`, `http_port`, `executable_name`, `args`, `env` (plain values only), `health_kind`, `health_path`, `deployment_seq`, `deployment_id` (`<nib_app_id>-dep-<deployment_seq>`), `archived`, `archived_at`, `created`, `updated`.
- **Validation** follows `internal/appspec`, the rules the node's renderer (`internal/render`) applies again before writing nibrunner's `desired.json` (plan 7.3), so the server refuses up front what the node would reject: name 1-80 characters of text; slug `^[a-z][a-z0-9-]{1,30}[a-z0-9]$`, not `drop`, `drop-dev`, `crew-dev`, `www`, `api`, `mail` or `canary`, unique (409 `slug_taken`) and immutable; `desired_state` `running`, `on-request` or `stopped`; `idle_timeout_ms` 60000-86400000; `vcpu` 1-2; `memory_mib` 128-4096; `disk_mib` 256-16384; `http_port` 1024-65535; `executable_name` `^[a-z][a-z0-9-]{0,31}$`; at most 64 `args` of at most 4096 bytes each and 4096 in all, no NUL; `health_kind` `tcp` (no path) or `http` (a path `/…` of printable characters, at most 1024; `/` by default); environment and secret names `^[A-Z][A-Z0-9_]{0,63}$`, not `HOME`, `PATH`, `NIBRUN_*` or `DROP_*`, a name either plain or secret, at most 64 together; values at most 4096 bytes, no NUL, and no `${NIBRUN_…}` reference other than `${NIBRUN_HOSTNAME}` and `${NIBRUN_HTTP_PORT}`; the plain environment at most 32 KiB as JSON. `data.field` names the input field.
- **`app.create`** defaults: `on-request`, 300000 ms, 1 vCPU, 512 MiB, 1024 MiB disk, port 8080, executable `server`, no arguments or environment, a `tcp` health check; deployment 1. The caller's non-archived apps must be fewer than its `app_quota` (default 3; admins are exempt), else 409 `quota_exceeded`. Operations run in one write transaction, so two creates cannot both take the last place.
- **`app.set`** changes the given fields (`args` replaces the list; `env` sets and unsets names, a name in both is refused). The slug is not an input (unknown field, 400). A change to the instance configuration bumps the deployment (below) and answers `deployment_bumped: true`. TODO(M7): the disk is refused (409 `disk_locked`) once the app has been placed.
- **`app.start`** sets `desired_state` back to the state the app had before it stopped (`running` or `on-request`, kept as `start_state`); **`app.stop`** sets `stopped`. Neither bumps the deployment.
- **`app.archive`** marks an app archived (it stops running once nodes exist, M7, and no longer counts against the quota; its data, secrets and slug stay; Drop deletes nothing). Writes to an archived app answer 409 `app_archived`; **`app.restore`** brings it back if the quota has a place (409 `app_not_archived` for an app that is not archived). `app.list` lists archived apps with `archived: true`; with `all: true` (admins only, else 403) it lists every account's. Its `quota` is `{used, limit, unlimited}` for the caller.

### Deployments

nibrunner acts on an instance only when its `deploymentId` changes: a changed configuration under the same id does nothing. So every change to what Drop renders into the instance gives the app the next `deployment_seq` (`internal/dropserver/deployment.go`):

- `app.set`, when one of the instance fields changes value: the run mode (`running` versus `on-request`, which decides the activation block), `idle_timeout_ms`, `vcpu`, `memory_mib`, `http_port`, `executable_name`, `args`, `env`, `health_kind`, `health_path`. Values are compared normalized (JSON re-encoded, an empty list or object the same as none), so setting a field to its current value bumps nothing.
- `secret.set` and `secret.delete`, always (a new version or a removed name changes the instance environment).
- `release.deploy` (M5b), when the release changes.

The name, `app.start`/`app.stop` (only the desired state, which nibrunner applies to the current deployment), the disk (the volume, fixed once placed) and archive/restore do not bump it. The node's `desired_seq` (M7) is a separate counter that goes up on every change to a node's rendered document, these included. A deployment number past 999999999 answers 409 `deployment_limit`.

## Secrets

A secret is an environment variable whose value lives only in SSM, at `<prefix>/apps/<app id>/<NAME>` (`awsx.SecretParameter`; the app id without `d-`). The server holds the store as an `awsx.SecretWriter` (put and delete, no read; `TestServerNeverReadsSecrets`), handed over by `serve` (`internal/client/serve_aws.go`, `dropserver.UseServices`). The database keeps `app_secret_names`: name, SSM version, the value's length, `set_via` and who set it ([Data model](data-model.md#app-secret-names)).

- **`secret.set`** checks the name and value (above), stores the value as a new version, records the name and bumps the deployment. The value is never stored, logged, returned or written to an event; a store error is logged with the value redacted and answers 502 `secret_store_failed` with nothing changed. `set_via` is `mcp` when the MCP tool `drop_write` (M6) made the call (it marks its in-process request with `dropserver.WithVia`), else `ui`; the reader marks secrets set through MCP, whose values passed through an assistant's conversation (decision 9).
- **`secret.list`** returns `{name, version, length, set_via, updated_by, updated}` per secret, never a value.
- **`secret.delete`** removes the name and every SSM version, and bumps the deployment; an unknown name is 404.
- The store call runs inside the operation's transaction (at most 15 s): a refused call changes nothing; a version stored by a call whose transaction then fails is never referenced.
- `TestSecretSentinel` sets a random value through every path (success, refusals, a failing store whose error echoes the value, an MCP-marked call) and checks that it appears in no database table, no operation answer, no `_logs` row and no standard log line.

## Events

Each successful mutating operation writes one `events` row in its transaction: `actor` (the caller), `action` (the operation name unless the operation says otherwise), `app` and `subject_user` when it acts on one, and `detail`, a JSON object of at most 8 KiB that never holds a secret value (names, lengths and before/after values of plain fields only). App events name what changed: `app.create` `{name, slug}`; `app.set` `{changed, env_set?, env_unset?, deployment_bumped, deployment_seq}` (environment names, never values); `app.start`/`app.stop` `{desired_state}`; `app.archive`/`app.restore` `{slug}`; `secret.set` `{name, version, set_via, deployment_seq}`; `secret.delete` `{name, deployment_seq}`. Read operations write nothing. Collection rules show admins every event and other accounts the events of their own apps.

## Adding an operation

1. **Contract.** Add a `Spec` to `specs` in `internal/dropapi/ops.go` (`Name` like `app.create`, `Role`, a one-line `Summary` that MCP shows, `Read` for operations that change nothing, `Input: &YourInput{}`, `OnceResult` for results shown only once, `SecretInput` for an input holding a secret value) and its input and result types beside it (JSON names in `snake_case`; pointer fields for optional changes).
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
6. **Clients.** Add the name to `OPS` in `ui/src/dropApi.ts` (`TestReaderKnowsEveryOperation` compares the lists), a CLI command if people need one (`internal/client`, building the request with `dropapi.Build`), and the operation to this page and the skill.
7. **Tests.** Cover the role (a user calling an admin operation gets 403), ownership (another account's record is 404), validation, the event and isolation with the `internal/droptest` helpers (`h.User`, `h.Invite`, `h.Op`, `h.MustOp`).
