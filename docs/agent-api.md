# Agent API

What a node's agent (`drop agent`, milestone M9) uses to talk to the Drop server: sign-in as its node, a realtime wake-up, the full desired document and the report. The wire types are `internal/dropapi/agent.go`; the server side is `internal/dropserver/agent_routes.go`, `desired.go`, `seq.go`, `reports.go` and `heartbeat.go`. `tests/fake_agent.py` implements the contract in Python and `tests/e2e.py` runs it against the real binary with `--fake-aws`.

## Transport

The agent runs on the host, beside the server, and speaks plain HTTP to the hosted listener (for example `DROP_AGENT_SERVER=http://127.0.0.1:8100`). Hosted mode checks the `Host` header exactly, so every request sends `Host: <the Drop domain>` (`DROP_AGENT_HOST`) and no `Origin` header. Every route below is on the hosted allowlist (`droppolicy.PublicRoute`, golden table `internal/droppolicy/testdata/public_routes.json`); nothing else is reachable with a node token.

## Nodes and credentials

An admin adds a node with `node.create {name, host_id}` (`drop node create NAME --host-id HOST_ID`, the reader's `#/admin/nodes`, or MCP `drop_write`). The answer `{node, password, identity, collection}` shows a generated 40-character password once (never stored for replay or written to an event); `node.rotate_secret` replaces it the same way. `host_id` is nibrunner's `hostId` from `reported.json`. Node states: `active` (takes placements), `draining` (keeps its apps and keeps being served, takes no new ones), `disabled` (cannot sign in; its current tokens end at once).

A node is an auth record of the `nodes` collection, never a `users` account. Its token reaches only the agent routes, its own `nodes` record and its own `placements`; `POST /api/drop/op` and `GET /api/drop/me` answer 403 `forbidden`; every other collection rule refuses it.

## Sign-in

| Request | Answer |
| --- | --- |
| `POST /api/collections/nodes/auth-with-password` `{"identity": "<node name>", "password": "<node secret>"}` | 200 `{token, record}` (`record.id` is the node id, `record.collectionName` `nodes`); 400 wrong name or password; 403 a disabled node |
| `POST /api/collections/nodes/auth-refresh` with `Authorization: <token>` | 200 `{token, record}`; 401 when the token is invalid or ended |

Tokens last 3600 s: refresh every 30 minutes. A 401 on any route means sign in again (with backoff); a 400 on sign-in means the password is wrong (rotated) and a 403 that the node is disabled: keep the current `desired.json` and retry slowly. The sign-in routes take bodies of at most 16 KiB and each client address gets 10 requests at once over both routes, then one every 6 seconds (429 `rate_limited`, `Retry-After`); PocketBase's own auth limit (2 password sign-ins per 3 seconds per address) applies as well (`TestNodeSignInRateLimit`). After the server restores a backup (`--hosted-restore`) every earlier node token gets 401: sign in again.

## Wake-up: realtime on `nodes/<id>`

1. `GET /api/realtime` (no token needed) opens a server-sent event stream; the first event, `PB_CONNECT`, carries `{"clientId": "..."}`.
2. `POST /api/realtime` with `Authorization: <token>` and `{"clientId": "...", "subscriptions": ["nodes/<node id>"]}` answers 204.
3. Each change to the node's record arrives as an event named `nodes/<node id>` with `{"action": "update", "record": {..., "desired_seq": N, ...}}`.

`desired_seq` goes up exactly when the node's desired document changes content (below). Heartbeat writes also update the record (at most every five minutes), so compare `desired_seq` with the one applied and pull only when it is higher. The hosted server closes long streams (its write timeout is 5 minutes) and proxies may too: reconnect with jittered backoff (1-30 s), resubscribe, and pull after every (re)connect. A 60-second safety pull and the report's answer (`desiredSeq`) cover missed events, so the design is level-triggered: losing an event delays a change, never loses it.

## The desired document: `GET /api/drop/agent/desired`

`Authorization: <node token>`. 200 with `drop.desired/v1` (`dropapi.Desired`):

```json
{
  "schema": "drop.desired/v1",
  "node": "walter-aws",
  "nodeId": "<node record id>",
  "hostId": "<nibrunner hostId>",
  "state": "active",
  "desiredSeq": 7,
  "contentHash": "<64 hex>",
  "apps": [
    {
      "appId": "d-<15>",
      "deploymentId": "d-<15>-dep-5",
      "desiredState": "on-request",
      "idleTimeoutMs": 300000,
      "volume": {"volumeId": "d-<15>-vol", "sizeBytes": 1073741824},
      "release": {"sha256": "<64 hex>", "s3Key": "releases/<sha256>", "sizeBytes": 1234567},
      "executableName": "server",
      "httpPort": 8080,
      "args": [],
      "resources": {"vcpu": 1, "memoryMib": 512},
      "healthCheck": {"kind": "tcp"},
      "hostnames": [{"hostname": "<slug>.<app domain>", "kind": "platform"}],
      "env": {"MODE": "demo"},
      "secrets": [{"name": "API_TOKEN", "parameter": "/drop/apps/<15>/API_TOKEN", "version": 2}]
    }
  ],
  "rejected": [{"appId": "d-<15>", "code": "invalid_app", "field": "resources.vcpu", "message": "..."}]
}
```

- **`apps`**: the apps placed on this node that are not archived and have a current release, sorted by `appId`. Each is `render.App` (the renderer's input, `internal/render`) except that secrets carry **no value**: the server holds no secret reader. The agent reads each secret from SSM at `parameter` and `version` (with decryption) and fills it in (`DesiredApp.RenderApp(values)`), stages the release (`s3Key`, refusing bytes whose size or sha256 differ) and calls `render.Render` with its own limits (`DROP_AGENT_ALLOWED_HOST_SUFFIX`, `DROP_SSM_PREFIX`, nibrunner's allocatable) and the current file as `Previous`. Validation limits are never sent: the server does not control the node's validation. A managed Crew app may carry `limits` `{rps, burst, maxObjectBytes, storedBytesQuota, monthlyBytesOut}` for the node's host services proxy (`docs/agent.md`). `storedBytesQuota` and `monthlyBytesOut` are the app's effective hard limits for stored bytes and bytes sent: its `usage_limits` override or the defaults, times ten. The node therefore enforces at once what the usage collector enforces only after the access logs arrive. Zero `rps`, `burst` and `maxObjectBytes` keep the node's defaults; any `limits` change bumps `desiredSeq`. The agent refuses an app whose limits are out of range.
- A managed Crew app may carry `replicaSyncSeconds` (1-300; omitted when unset): the agent validates it and injects `DROP_REPLICA_SYNC_INTERVAL` (`<n>s`) into the guest. Anything else, or the field on an app that is not managed Crew, rejects the app alone. An agent older than 0.3.0 refuses a document carrying the field, so upgrade agents before the server.
- A Crew app carries the service-owned `crewOwnerEmail` copied at creation. `signInEmailEnabled` defaults to false when absent. The agent validates the address and injects it as the reserved guest environment variable `DROP_OWNER_EMAIL`. When enabled on a runnable app it also emits `instances[].signInEmail.recipient` with exactly that address. Upgrade NibRunner and its agent before enabling Crew placements. Ordinary apps need no mail configuration.
- **Gone apps**: an app the node ran before that is in neither `apps` nor `rejected` (archived, unplaced, moved away, or no release) becomes a stopped tombstone with its volume kept (`render` does this for appIds of `Previous` missing from the input). Nothing is ever `absent` in v0.
- **`rejected`**: apps the server's own check refused (it runs `render.Render` over the same apps with its limits, no secret values and the node's last reported allocatable): put their appIds in `render.Input.Keep` (keep the previous entry unchanged, never tombstone them) and report them `failed` with an agent-generated message.
- **Per-app failures on the node** (staging, SSM, the node's own render check): keep the previous entry (`Keep`) or leave a new app out, never reference an unstaged artifact; report `staging_failed`, `secret_failed` or `failed`.
- **`desiredSeq`** is the node's counter; one `desiredSeq` always means one content (when the stored hash is stale the route bumps first). **`contentHash`** is the hex sha256 of the document's JSON with `desiredSeq`, `state` and `contentHash` empty; it is not the rendered file's hash (`render.Output.ContentHash` is). The revision the agent writes is `render`'s `drop-<desiredSeq>-<12 hex>`.
- **`state`**: `active` or `draining` (both are served the same way).

Errors (body `{status, code, message, data}`): 401 `unauthenticated` (no or ended token: sign in again), 403 `forbidden` (not a node token), 403 `node_disabled`, 429 `rate_limited` (`Retry-After`; each node gets 30 requests at once, then one a second), 503 `desired_unavailable` (keep what runs, retry). On any failure or a malformed body, write nothing and retry; a malformed document as a whole is refused, a bad app alone.

## The report: `POST /api/drop/agent/report`

`Authorization: <node token>`, a JSON body of at most 1 MiB, every 30 seconds and after each write of `desired.json`. `drop.report/v1` (`dropapi.Report`), unknown fields refused:

```json
{
  "schema": "drop.report/v1",
  "hostId": "<nibrunner hostId>",
  "agentVersion": "0.1.0",
  "desiredSeq": 7,
  "acceptedRevision": "drop-7-0123456789ab",
  "nibrunner": {
    "state": "ready",
    "message": "",
    "reportedAt": "2026-11-20T10:00:00Z",
    "capacity": {"vcpuCount": 8, "memoryMib": 16384, "cacheBytes": 1073741824},
    "allocatable": {"vcpuCount": 6, "memoryMib": 12288, "cacheBytes": 1073741824},
    "versions": {"agent": "...", "firecracker": "...", "guestImage": "...", "zerofs": "..."}
  },
  "apps": [
    {"appId": "d-<15>", "deploymentId": "d-<15>-dep-5", "phase": "running", "message": "",
     "restartCount": 0, "lastExitCode": null, "convergedAt": "2026-11-20T09:59:00Z"}
  ]
}
```

- `hostId` must be the node's (409 `host_mismatch` otherwise: the agent is on the wrong host and must stop reconciling). `agentVersion` 1-64 of `[A-Za-z0-9._+-]`. `desiredSeq` is the seq of the last document applied; `acceptedRevision` nibrunner's.
- `nibrunner` is the host part of `reported.json`, or `null` when the agent could not read it. Never send `cron` (it holds tenant environment values), checkpoints or exports. `capacity`, `allocatable` and `versions` are at most 2 KiB of JSON each; a new `allocatable` can change which apps pass the server's check (and bump `desiredSeq`).
- `apps` (at most 256, each appId once): `phase` is one of `pending`, `staging`, `staging_failed`, `secret_failed`, `deploying`, `starting`, `running`, `sleeping`, `unhealthy`, `stopped`, `failed`, `unknown` (mapping from nibrunner in plan 7.7); `deploymentId` `<appId>-dep-<n>` or empty; `message` for people (never secret or environment values; cut to 2000 characters); `lastExitCode` an integer or `null`; `convergedAt` RFC 3339 or empty. Optional `lastHook` carries `event` (suspend, resume or terminate), `outcome` (ok, error, timeout or unreachable), RFC 3339 `time` and nonnegative `durationMs`. Optional `usage` `{deploymentId, seq, counters: {readRequests, writeRequests, listRequests, deleteRequests, rejectedRequests, bytesIn, bytesOut}}` holds the host services proxy's monotonic counters for the app's `deploymentId`. The server takes it only for a new deployment or a higher `seq`, adds the difference to `node_reports.host_usage_total` and writes usage on its own at most every five minutes. Apps not placed on this node are ignored (listed in the answer).
- 400 `invalid_request` (not a report, unknown fields, over 1 MiB) or `invalid_input` with `data.field`: fix the agent, do not retry the same body. 401, 403, 429 as for the desired route.

200 answers `{"desiredSeq": N, "state": "active", "accepted": 1, "ignored": []}`: pull again when `desiredSeq` is above the one applied.

**Writes.** The server keeps each node's last report time in memory and writes the node's fields (`agent_version`, `accepted_revision`, `host_state`, `capacity`, `allocatable`, `versions`, `last_heartbeat`, `last_report_at` from `reportedAt`) only when they change or five minutes after the last write; `node_reports` rows only when an app's phase, message, deployment, restart count, exit code or convergence time changes. A steady node costs one database write every five minutes (`TestHeartbeatWritesAtMostEveryFiveMinutes`). A phase change writes an `app.phase` event with `actor_node` (not between `running`, `sleeping` and `starting`).

## What people see

`status {app}` (`drop app status APP`, the app's Overview in the reader, MCP `drop_read` `what=status`) gives the phase: `archived`, `no_release` or `unplaced` before a node is involved; `pending` until the node reports the app; the reported phase; `deploying` while the node reports an older deployment (unless it failed); `unknown` when the node has not reported for 90 seconds. Admins also see the node's name. `node.list` (`drop node list`, `#/admin/nodes`, `what=nodes`) shows each node's state, whether it is online (a report in the last 90 seconds), `desired_seq`, nibrunner's numbers and its app count.

The optional desired `deleted` array names app IDs awaiting confirmed volume deletion on this node. The report echoes an ID in `deleted` only after NibRunner accepts the matching revision, removes the instance and reports the volume deleted or absent. The server accepts acknowledgements only for this node and current desired sequence.

### Awake accounting and hourly comparison

The server timestamps a sample from the authenticated node's `nibrunner.reportedAt`. It settles each placed deployment about once a minute or on phase transitions. Running and starting are awake. With phase-only reports the last observed phase spans missing reports. A node with the persistent per-deployment counter sends `apps[].awakeCounter` with nonnegative integer `measuredSeconds` and `billableSeconds`. Billable time may be lower when a platform shutdown takes time. The server settles the increase between observations and retains overlapping sample estimates. Stable timestamps and durable baselines make retries safe across process restarts.

For the one-day comparison, query a consistent operator snapshot after the window closes. Bind the PocketBase node record ID and UTC Unix start second. Each row covers two consecutive comparable reports; the query prorates a partial boundary interval by its overlap with the day. A phase-only sample spanning an outage cannot identify transitions between observations. Long intervals make that uncertainty visible in `longest_gap_seconds`.

```sql
WITH intervals AS (
  SELECT *, (MIN(until, :start + 86400) - MAX(since, :start)) * 1.0
              / (until - since) AS fraction
  FROM drop_credit_comparison
  WHERE node = :node AND until > :start AND since < :start + 86400
)
SELECT app, deployment,
       ROUND(SUM(sampled * fraction), 2) AS phase1_seconds,
       ROUND(SUM(measured * fraction), 2) AS measured_seconds,
       ROUND(SUM(billable * fraction), 2) AS phase2_seconds,
       MAX(until - since) AS longest_gap_seconds
FROM intervals
GROUP BY app, deployment;
```
