# Connect an assistant with MCP

Hosted Drop offers a remote Model Context Protocol (MCP) endpoint at `https://HOST/mcp`, with OAuth (automatic client registration, S256 PKCE, endpoint-bound tokens). It is part of hosted mode ([Hosting](hosting.md)): `serve --hosted` requires `--mcp`. The MCP and OAuth implementation comes from pocketcontext-core.

## Tools

Drop has exactly two tools, `drop_read` and `drop_write` (decision 11). Both act with the account that approved the connection and reach Drop only through its operation route, `POST /api/drop/op` ([Operations](ops.md)), in-process: the server checks the account, its role and ownership on every call, exactly as for the reader and the CLI. No tool runs shell commands, reads host files or administers the server. Future features add operations or `what` values, not tools; a new tool needs the owner's approval.

Each MCP request reads `GET /api/drop/me` with the caller’s token and selects the tool descriptions and schemas for the account role and the official catalog setting. Private deployment requires the developer role. An existing OAuth connection picks up a grant or revocation on its next request. The API checks the current account again when executing the operation.

### drop_read

`drop_read {what, app?, all?, limit?, offset?}`, annotated `readOnlyHint`. Each `what` is one read operation (the table `dropReads` in `internal/client/drop_mcp.go`); `app` is an app's id or slug, `all: true` asks for admins' everyone-wide lists, and lists page with `limit` (1-500, default 100) and `offset` and answer `has_more`. An argument the operation does not take, or a missing required one, is refused before any call.

| `what` | Role | Operation | Reads |
| --- | --- | --- | --- |
| `me` | user | `me` | the account: email, role, developer access, app quota, admin, owner |
| `apps` | user | `app.list` | the caller's apps and quota; `all` (admins) everyone's |
| `app` | user | `app.get` | one app (`app`) with its secret names |
| `releases` | user | `release.list` | an app's releases (`app`); not available until releases exist |
| `secrets` | user | `secret.list` | an app's secrets (`app`): `name`, `length`, `updated`, `set_via` only |
| `usage` | user | `usage.get` | an app's S3 usage (`app`): the last 24 hours by hour, the month to date against its soft and hard limits, stored bytes and an estimated cost |
| `status` | user | `status` | an app's runtime status (`app`): phase, message, deployment, restarts; the node's name for admins |
| `events` | user | `event.list` | events of the caller's apps or of one app (`app`); `all` (admins) every event |
| `users` | admin | `user.list` | every account |
| `invites` | admin | `invite.list` | invites and their state |
| `nodes` | admin | `node.list` | nodes (hosts): state, online, desired_seq, capacity and app count |

A `what` whose operation is not registered yet stays listed, marked "(not available yet)" in the description, and answers `not_available`. Registering the operation under that name, as a read, makes it work with no MCP change.

### drop_write

`drop_write {op, input?, idempotency_key?}`, annotated `destructiveHint` (the enum includes `app.stop` and `secret.delete`): a thin pass-through to `POST /api/drop/op`. `op` is an enum of the caller's operations, `input` the operation's object (default `{}`), and `idempotency_key` replays the first result of a retried write for 24 hours (`secret.set` ignores keys). The answer is `{op, result, replayed?}`.

The op enum and the op table in the tool description (name, role, input signature such as `{app, name?, env?: {set?, unset?}}` and summary) are generated from the registry in `internal/dropapi`: the operations whose `Spec` has `MCP: true`, filtered to the role and capabilities of the account. `TestMCPWriteTableMatchesRegistry` checks the enum and table against the registry, and `TestMCPWriteOpsFollowTicket` checks the set against decision 11's list:

- users: manage their own published catalog apps with `workspace.create`, `workspace.resize` and `workspace.upgrade`;
- developers: user operations plus custom apps, including build inspection, custom app creation and release submission;
- admins: `invite.create`, `invite.revoke`, `user.set`, `node.create`, `node.rotate_secret`, `node.set_state`, `placement.set`, `placement.clear`.

Every operation on these lists is registered (`node.create` and `node.rotate_secret` return a node's agent password once: it passes through the conversation, so the instructions ask admins to hand it only to whoever installs the agent). Reads sent to `drop_write` are refused (use `drop_read`), and so are operations not offered through MCP (`app.archive`, `app.restore`).

### Test a custom app

1. Call `drop_read` with `{"what":"me"}`. Check that the account role is `developer`.
2. For a local executable or a tar.gz archive containing one Linux amd64 executable, call `developer.build_upload {}` and PUT the bytes to the one-time URL outside the conversation. For a public HTTPS download, call `developer.build_import {"url":"…"}`. Both paths inspect the executable inside the archive and return its digest for creation. The executable must implement `--drop-manifest`.
3. Read the inspected product, version, recommended multiplier and SHA-256. Select a size from 1× through 8×. Drop obtains the runtime settings from the executable’s `--drop-manifest`.
4. Call `developer.build_create {"sha256":"DIGEST","name":"Upgrade test","multiplier":4}` with a stable idempotency key. Read the returned app ID and hostname. Custom instances receive a generated `dev-` prefix. Visit the HTTPS root of the returned hostname to open the app. Use `drop_read` with `{"what":"status","app":"APP_ID"}` to follow deployment.

Developer operations remain available when the catalog is active. Publishing a custom build to the catalog requires a separate admin review.

### Errors

A refused call is a tool error whose text is `{"error": {"status", "code", "message", "field"}}`: the server's status, stable code and message (for example 404 `not_found` for another account's app, 403 `forbidden` for an admin operation from a user), or a local `invalid_arguments`, `not_available`, `limit_exceeded` or `timeout`. A protocol error of `tools/call` (an unknown tool, malformed arguments) has a fixed message.

### Secrets (decision 9)

`secret.set` through `drop_write` is write-only. The tool marks its in-process request with `dropserver.WithVia(req, "mcp")`, so the secret's `set_via` is `mcp` and the reader marks it "Set through MCP" with Replace. The value passes through the assistant's conversation, which the `drop_write` description and the server instructions say, preferring the reader's Env & secrets tab. It never comes back: results keep only a secret's `name`, `length`, `updated` and `set_via` (`drop_read what=secrets`, `what=app` and the `secret.set` result alike); tool errors carry only Drop's own error fields, which never quote an input; tools decode their own arguments, so no argument is echoed in an error; and the observe diagnostics hold only fixed labels. `TestMCPSecretSentinel` sets a random value through MCP, along with refusals, argument errors and a failing store whose error echoes the value, and checks that it appears in no MCP answer, no diagnostic line, no log line and no database table.

## Diagnostics

Each MCP request writes metadata-only JSON lines to standard error (pocketcontext-core `mcpobserve`): method, tool (`drop_read`, `drop_write` or `unknown`), outcome, a fixed reason, the API route label (`/api/drop/op` only), status, counts and durations. The lists are `droppolicy.Observe`. No argument, input, result or error message is ever logged.

## Connect and consent

1. Sign in to the hosted reader in the browser profile that will approve the connection.
2. Copy the address from the reader's Connect page and add it as a custom MCP server in the assistant's connector settings. Leave manual client credentials empty.
3. The assistant opens Drop's consent page. If you are not signed in there, Log in returns you to it after the email sign-in. Approve the request.

A grant belongs to the account that approved it and gets only what that account may do. Each account may hold 10 connections; accounts other than the owner stop short of the instance limits, so the owner can always connect.

## Native desktop clients

Native clients can register HTTP callbacks on literal `127.0.0.1`, `[::1]` or exactly `localhost` with their chosen port, and private schemes based on a reversed domain, such as `com.example.app:/oauth/callback`. Other public callbacks must use HTTPS. For an intended client that needs a legacy native scheme, set `DROP_MCP_REDIRECT_EXCEPTIONS` to a JSON array of exact callback URIs in the private service configuration and restart. Malformed values fail startup.

## Operate and revoke

OAuth state is machine-local in the private cache, separate from the database. The reader's Connect page lists the account's connected apps and disconnects one (its tokens stop working at once); a browser visit to `/oauth/connections` leads there.

On an installation with the official catalog enabled, read `what: "catalog"`
to see the published releases. Customer writes offer
`workspace.create`, `workspace.resize` and `workspace.upgrade`. Creating an app needs a product, a name and a 1–8
multiplier. Upgrading requires the app ID and the exact reviewed digest. Use a
stable idempotency key when retrying an operation.

Use `drop_read` with `what: "backups"` to list your portable Crew archives, schedules and recent jobs. `drop_write` offers `backup.configure`, `backup.create` and `backup.restore` to customer accounts. Follow a restore job until it reports completion. The authenticated download route delivers archive bytes directly to the owner.
