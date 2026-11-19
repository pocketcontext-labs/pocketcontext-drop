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
| 409 | `account_exists`, `invite_exists`, `invite_closed`, `idempotency_conflict`, `idempotency_result_unavailable` |
| 413 | `invalid_request` (body too large) |
| 500 | `internal` (details only in the server log, never the input) |

### Idempotency keys

A key is 1-128 letters, digits, `.`, `_`, `:` or `-`, and belongs to the calling account. The first successful call stores the key with a hash of the operation and its input, and the result. A retry with the same key and input within 24 hours replays that result without acting again (no second change, no second event). The same key with another operation or input answers 409 `idempotency_conflict`. A failed call stores nothing, so the key stays usable. An operation whose result is shown only once (a generated credential, from M7) stores no result: its replay answers 409 `idempotency_result_unavailable`. Read operations ignore keys. Keys live in the private `op_keys` collection (hashes only; every rule closed) and are pruned hourly.

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

- **Lists**: `limit` 1-500 (default 100), `offset` 0-10000; `has_more` says more follow.
- **An account** (`user`): `id`, `email`, `role`, `app_quota`, `suspended`, `owner`, `signed_in` (false until its first sign-in; core prunes such accounts after 15 minutes), `invited_by`, `created`.
- **`user.set`** names the account by id or email and needs at least one change. `app_quota` is 0-100. Nobody changes their own role or suspension, and the owner's role and suspension cannot change (403 `forbidden`). Suspending ends every session of the account at once (its token key changes) and a suspended account cannot sign in (the users auth rule is `suspended = false`).
- **An invite**: `id`, `email`, `role`, `app_quota`, `state` (`open`, `used`, `revoked` or `expired`), `invited_by`, `expires_at`, `used_at`, `used_by`, `revoked_at`, `created`.
- **`invite.create`** takes one plain ASCII address (at most 254 characters, lower-cased), role `user` by default, `app_quota` 3 by default (0-100) and `expires_days` 7 by default (1-30). An address with an account answers 409 `account_exists`, one with an open unexpired invite 409 `invite_exists`; an expired open invite is closed and replaced. The result's `sign_in_url` is `https://HOST/#/invite?email=…`: the reader's sign-in page with the address filled in. It carries no credential. **No invite email is sent** (decision 16): the admin shares the link; the invitee's first sign-in sends the ordinary sign-in code email, which creates the account with the invite's role and quota and marks the invite used.
- **`invite.list`** takes `state` `all` (default) or `open` (unused, unrevoked and unexpired).
- **`invite.revoke`** closes an open invite (expired or not); a used or revoked one answers 409 `invite_closed` (suspend the account instead).

## Events

Each successful mutating operation writes one `events` row in its transaction: `actor` (the caller), `action` (the operation name unless the operation says otherwise), `app` and `subject_user` when it acts on one, and `detail`, a JSON object of at most 8 KiB that never holds a secret value (names, lengths and before/after values of plain fields only). Read operations write nothing. Collection rules show admins every event and other accounts the events of their own apps.

## Adding an operation

1. **Contract.** Add a `Spec` to `specs` in `internal/dropapi/ops.go` (`Name` like `app.create`, `Role`, a one-line `Summary` that MCP shows, `Read` for operations that change nothing, `Input: &YourInput{}`, `OnceResult` for results shown only once) and its input and result types beside it (JSON names in `snake_case`; pointer fields for optional changes).
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
