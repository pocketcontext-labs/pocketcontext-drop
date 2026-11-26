# Node agent

`drop agent` runs as root on each nibrunner host, beside the Drop server. It is the only writer of nibrunner's `desired.json`: it signs in as its node, follows the node's desired document (`docs/agent-api.md`), stages releases, reads secret versions, renders the whole file with `internal/render` (`docs/render.md`) and reports what nibrunner runs. The code is `internal/agent`; the CLI wiring is `internal/client/agent.go`; the unit is `deploy/walter-aws/drop-agent.service`.

```
drop agent run      follow the desired document and write desired.json (the service)
drop agent check    run the start checks; writes nothing
drop agent render --input FILE|- [--previous FILE]
                    render a drop.desired/v1 document offline (secret values REDACTED)
```

`drop agent` never resolves a project, opens a database, starts a server, reads the local key or uses remote mode (`--db`, `--url` and `DROP_URL` do not apply).

## Safety rules

- **Drop owns the whole `desired.json`** (decision 12). If the file holds any entry (instance, volume or export) whose `appId` does not start with `d-`, `run` refuses to start and `check` fails, both with exit 3, naming the entries; nothing is written. The agent never deletes what it did not create: the operator removes those entries (and their volumes), lets nibrunner converge, then starts the agent. A foreign entry that appears while the agent runs stops it the same way. A file Drop cannot read as its own (checkpoints, exports, unknown fields) is refused the same way.
- **No write without a good answer.** When the server is unreachable, times out (10 s), answers an error (401 after a new sign-in, 403 `node_disabled`, 429, 5xx) or sends a malformed document, the agent leaves `desired.json` unchanged, records the failed pull in `state.json` and retries. A disabled node or a wrong (rotated) secret keeps the file and retries once a minute.
- **Wrong host.** If the server's node is for another `hostId` than the one in `desired.json` (or `reported.json`), or a report is answered 409 `host_mismatch`, the agent stops with exit 3.
- **The server never chooses paths, commands or parameters.** Every app is validated on the node (plan 7.3, `internal/render`); a release is fetched only from `releases/<sha256>` and refused unless its size and sha256 match; a secret is read only from `<DROP_SSM_PREFIX>/apps/<appId without d->/<NAME>`. A bad app fails alone.
- **Never an unstaged artifact.** When staging or a secret fails for an app, its previous entry is kept unchanged (`render.Input.Keep`), or a new app is left out; the report says `staging_failed` or `secret_failed`. Apps the server refused (`rejected`) keep their previous entries and are reported `failed`. An app that is gone becomes a stopped tombstone with its volume kept; the explicit `deleted` list from a confirmed permanent deletion alone can mark its locally known volume `absent`.
- **Secrets** live in memory (cached by deployment, parameter and version) and in `desired.json` only. They never reach `state.json`, a report, a log line or an error (errors name parameters, never values); the agent leaves free-text host and instance diagnostics on the node. The node secret is read from a file, never from an argument or an environment variable.

## Crew sign-in email

NibRunner uses the last desired state it applied, including across restarts while Drop is unavailable. A stopped or tombstoned instance has no sign-in email binding. Disabling delivery removes the binding when the node applies the update. A node disconnected before a move can still use its old binding. Fence the source before activating the destination when exclusive delivery is required. Configure the SMTP relay, sender and credential on NibRunner. The guest receives its fixed owner email through `DROP_OWNER_EMAIL`.

## Host services

With `DROP_AGENT_HOST_SERVICES=s3`, `drop agent run` gives every managed Crew app the node's S3 proxy instead of a workload identity token. Those apps' nibrunner entries carry `hostServices: {"s3": true}` and no `AWS_WORKLOAD_IDENTITY`. The agent removes their token and role files. Before writing `desired.json`, it writes `<state dir>/host-services.json` (`drop.host-services/v1`, 0600): each such app's deployment, replica bucket, region and limits from the desired document. Each report carries the proxy's counters for the app's deployment (`usage`, read from `<state dir>/host-services-usage.json`).

`drop agent host-services` is a separate process with its own unit (`deploy/drop-ax41/drop-agent-host-services.service`) and the same environment file. It listens on `DROP_HOST_SERVICES_SOCKET` (default `/run/drop-agent/host-services.sock`, 0600 in a 0700 directory). It accepts only `DROP_HOST_SERVICES_PEER_UID` (nibrunnerd, default 0) through `SO_PEERCRED`. nibrunner relays each guest connection from vsock 51007 there, after one line `CONNECT <appId> <deploymentId>`. The proxy serves a deployment only while nibrunner's `desired.json` runs it with `hostServices.s3` and `host-services.json` names it. Otherwise it answers 403, at the handshake and on every request.

| Rule | Enforcement |
| --- | --- |
| Credentials | The guest holds placeholder keys. The proxy drops the guest's `Authorization`, date, token, ACL, grant, storage-class, tagging, object-lock, encryption and redirect headers. It signs again with the app's STS session, from `AssumeRoleWithWebIdentity` with a token it signs with the node key (`app_id` session tag). Sessions are cached per app until five minutes before they expire. |
| Scope | Path-style requests only. The bucket must be the app's replica bucket, and every key, list prefix, `start-after` and DeleteObjects key must start with `<appId>/`. Keys may not contain empty, `.` or `..` segments or control characters. Version IDs and copies are refused. |
| Operations | GetObject, HeadObject, PutObject, DeleteObject, DeleteObjects, ListObjectsV2 and multipart upload (create, part, complete, abort). Anything else is `NotImplemented`. |
| Payloads | A hex `x-amz-content-sha256` passes through, so S3 verifies the bytes. `UNSIGNED-PAYLOAD` and `STREAMING-UNSIGNED-PAYLOAD-TRAILER` pass through. Signed aws-chunked bodies are decoded and sent unsigned. Their trailer form is refused. `Content-Length`, `Content-MD5`, `x-amz-checksum-*`, conditional headers and `x-amz-meta-*` are kept. Uploads use `Expect: 100-continue`. |
| Rate | Token bucket per app: `limits.rps` (default 20 per second), burst `limits.burst` (default 200). `503 SlowDown` when exceeded. Each app may hold 32 connections and the node 512. |
| Size | `limits.maxObjectBytes` (default 100 MiB) per PutObject and per multipart object. Exceeding it gives `400 EntityTooLarge`. |
| Quota | `limits.storedBytesQuota` against a listing of the app's prefix, relisted every 15 minutes and kept current by the proxy's own writes. Exceeding it gives `403 QuotaExceeded`. |
| Download budget | `limits.monthlyBytesOut` per UTC month, counted on the node. Exceeding it gives `403 DownloadBudgetExceeded`. |
| Usage | Monotonic counters per deployment: read, write, list, delete and rejected requests, and bytes in and out. They are flushed every 5 seconds, and `seq` goes up with every flush that changed them. |

## The loop

1. **Start:** read `desired.json` (refuse foreign entries, learn `hostId`; a missing file is fine when `reported.json` names the host) and `state.json` (rebuilt when missing or unreadable).
2. **Sign in** as the node (`auth-with-password`), refresh every 30 minutes, sign in again on a 401 (at most every 5 seconds).
3. **Realtime:** `GET /api/realtime`, subscribe to `nodes/<id>`; reconnect with jittered backoff of 1–30 s (the hosted server closes streams after 5 minutes).
4. **Reconcile** on start, on every (re)connect, on every event whose `desired_seq` is above the one applied, when a report answers a higher `desiredSeq`, and every `DROP_AGENT_PULL_INTERVAL` (60 s):
   1. `GET /api/drop/agent/desired` (strict decoding; the envelope must name this node and host, include both the apps and rejected arrays, and match its content hash);
   2. stage each app's release into `DROP_AGENT_ARTIFACTS/<sha256>` (download to a private `.dl-*` file while hashing, refuse a size or sha256 mismatch, fsync, rename, fsync the directory; an unchanged file is trusted by size and mtime, any other one is hashed again);
   3. read each secret version from SSM;
   4. render with the node's limits (`DROP_AGENT_ALLOWED_HOST_SUFFIX`, `DROP_SSM_PREFIX`, nibrunner's reported allocatable) and the current file as the previous document;
   5. if the content hash differs from the current file's, copy the current file to `desired.json.drop-prev` and write the new one atomically (temporary file, fsync, rename, fsync the directory, mode 0600). A file that changed while rendering is not overwritten. An unchanged content (only `desiredSeq` moved) is not rewritten;
   6. update `state.json` and send a report.
5. **Report** (`POST /api/drop/agent/report`) after each reconcile and every `DROP_AGENT_REPORT_INTERVAL` (30 s).

## Report mapping

The agent decodes only these fields of nibrunner's `reported.json`: `hostId`, `state`, `reportedAt`, `acceptedRevision`, `capacity` and `allocatable` (`vcpuCount`, `memoryMib`, `cacheBytes` only), `versions` (`agent`, `firecracker`, `guestImage`, `zerofs`, cut to 128 bytes) and per instance `appId`, `deploymentId`, `state`, `restartCount`, `lastExitCode`, `convergedAt` and `lastHook`. The last hook allows only its event, outcome, RFC 3339 time and duration in milliseconds. Guest response text is excluded. `cron` (tenant environment values), checkpoints, exports, compute, addresses and restart details are never read, so they can never be sent. A `reported.json` for another host counts as missing.

Phase of each app the agent was given (`deploymentId` is the one nibrunner runs, empty until it runs one):

| Condition | Phase |
| --- | --- |
| the server refused it, or the node's render refused it | `failed` (with an agent-generated message) |
| its release could not be staged | `staging_failed` |
| a secret version could not be read | `secret_failed` |
| no readable `reported.json` | `unknown` ("nibrunner report unavailable") |
| `reportedAt` older than 60 s | `unknown` ("nibrunner report stale") |
| not in nibrunner's instances yet | `pending` |
| another deployment, or nibrunner has not accepted the revision written | `deploying` |
| `pending`, `starting` | `starting` |
| `running` | `running` |
| `idle` | `sleeping` |
| `unhealthy` | `unhealthy` |
| `stopping`, `stopped` | `stopped` |
| `failed` | `failed` |

Report shape (`drop.report/v1`, `internal/dropapi/agent.go`):

```json
{
  "schema": "drop.report/v1",
  "hostId": "walter-dev",
  "agentVersion": "0.1.0",
  "desiredSeq": 7,
  "acceptedRevision": "drop-7-0123456789ab",
  "nibrunner": {"state": "ready", "message": "", "reportedAt": "2026-11-20T10:00:00Z",
                "capacity": {"vcpuCount": 2, "memoryMib": 7136, "cacheBytes": 1073741824},
                "allocatable": {"vcpuCount": 1, "memoryMib": 6624, "cacheBytes": 1073741824},
                "versions": {"agent": "...", "firecracker": "...", "guestImage": "...", "zerofs": "..."}},
  "apps": [{"appId": "d-<15>", "deploymentId": "d-<15>-dep-5", "phase": "running", "message": "",
            "restartCount": 0, "lastExitCode": null, "convergedAt": "2026-11-20T09:59:00Z"}]
}
```

## State file

`<state dir>/state.json`: `DROP_AGENT_STATE_DIR`, else systemd's `$STATE_DIRECTORY`, else `/var/lib/drop-agent`, so on a host it is **`/var/lib/drop-agent/state.json`**. Mode 0600 in a 0700 directory, written atomically, pretty-printed JSON, schema `drop.agent-state/v1`. It holds metadata only — never a secret value, the node secret or a token (unit and end-to-end tests scan it) — and the agent can lose it: `desired.json` stays the truth about what runs.

```json
{
  "schema": "drop.agent-state/v1",
  "node": "walter-aws",
  "nodeId": "<node record id>",
  "hostId": "walter-dev",
  "lastSeq": 7,
  "lastRevision": "drop-7-0123456789ab",
  "lastContentHash": "<64 hex>",
  "lastPullAt": "2026-11-20T10:00:00Z",
  "lastWriteAt": "2026-11-20T09:58:00Z",
  "lastReportAt": "2026-11-20T10:00:00Z",
  "lastError": "",
  "apps": [{"appId": "d-<15>", "deploymentId": "d-<15>-dep-5", "phase": "rendered"}],
  "artifacts": {"<sha256>": {"size": 5836924, "mtimeUnixNano": 1795135487087317435}}
}
```

`apps[].phase` is the agent's own verdict at the last pull: `rendered`, `kept` (a previous entry kept; `message` says why), `staging_failed`, `secret_failed`, `failed` or `tombstoned`. `lastError` is the last pull failure (codes, appIds and parameter names only), empty after a good pull.

## Configuration

Environment (`/etc/drop/agent.env`, example `deploy/walter-aws/agent.env.example`); each variable has a `drop agent` option of the same name in lower case without the prefix (for example `--allowed-host-suffix`):

| Variable | Meaning |
| --- | --- |
| `DROP_AGENT_SERVER` | The Drop server, default `http://127.0.0.1:8100`. Remote servers require HTTPS. |
| `DROP_AGENT_HOST` | The Drop domain, sent as the `Host` header (required) |
| `DROP_AGENT_NODE` | The node's name (required) |
| `DROP_AGENT_SECRET_FILE` | The node secret file; default `$CREDENTIALS_DIRECTORY/node-secret` (systemd `LoadCredential=node-secret:/etc/drop/agent.secret`). One line, 32–256 characters, not readable by group or others |
| `DROP_AGENT_DESIRED`, `DROP_AGENT_REPORTED`, `DROP_AGENT_ARTIFACTS` | nibrunner's `desired.json`, `reported.json` and artifact store (absolute paths; on walter-aws under `/var/lib/nibrunner`) |
| `DROP_AGENT_STATE_DIR` | Where `state.json` lives (above) |
| `DROP_AGENT_ALLOWED_HOST_SUFFIX` | Every app hostname must end with it, for example `.bigconfig.space` |
| `DROP_SSM_PREFIX` | Secret parameter prefix, default `/drop` |
| `DROP_AWS_REGION`, `DROP_S3_BUCKET` | S3 and SSM through the default credential chain (`docs/aws.md`) |
| `DROP_AGENT_PULL_INTERVAL`, `DROP_AGENT_REPORT_INTERVAL` | Seconds, default 60 (5–3600) and 30 (5–300) |
| `DROP_AGENT_HOST_SERVICES` | Empty, or `s3` to give managed Crew apps the node's S3 proxy (above) |
| `DROP_HOST_SERVICES_SOCKET`, `DROP_HOST_SERVICES_PEER_UID` | `drop agent host-services`: its socket (default `/run/drop-agent/host-services.sock`) and nibrunnerd's uid (default 0) |
| `DROP_HOST_SERVICES_STS_REGION`, `DROP_HOST_SERVICES_S3_ENDPOINT`, `DROP_HOST_SERVICES_STS_ENDPOINT` | STS region (default `DROP_AWS_REGION`). The endpoint overrides are for offline tests: HTTPS, or HTTP on loopback only. The host-services process also needs `DROP_IDENTITY_ISSUER`, `DROP_IDENTITY_ROLE_ARN` and `DROP_IDENTITY_KEY_FILE`. |

`--fake-aws DIR` reads releases and secrets from the directory a `serve --fake-aws DIR` writes (`docs/aws.md`), only with a loopback server URL, for tests.

Exit codes: 0 stopped (SIGTERM or interrupt) or check passed, 1 failure, 2 usage or configuration, 3 refused (operator action needed). The unit's `RestartPreventExitStatus=3` keeps systemd from restarting into the same refusal.

## `check` and `render`

`drop agent check` prints `{"ok", "refused", "node", "hostId", "checks": [{"name", "ok", "message"}]}`: `desired.json` (foreign entries, a document Drop can take over, the host), `reported.json`, the artifacts and state directories, the node secret, sign-in, the server's desired document for this host, each placed app's release in S3 (HeadObject) and each secret version in SSM (read and discarded). On a host, run it with the service's environment and credential:

```sh
sudo systemd-run --pipe --wait --collect -p EnvironmentFile=/etc/drop/agent.env \
  -p LoadCredential=node-secret:/etc/drop/agent.secret /usr/local/bin/drop agent check --pretty
```

`drop agent render --input FILE|-` reads a `drop.desired/v1` document (as `GET /api/drop/agent/desired` returns it) and renders it against `--previous FILE` (default `DROP_AGENT_DESIRED`) with every secret value `REDACTED`. The preview also redacts previous environment values whose public value cannot be confirmed from the input. It runs offline and prints `{"revision", "contentHash", "changed", "rejected", "kept", "tombstoned", "document"}` and writes nothing.

## systemd

`deploy/walter-aws/drop-agent.service`: `Type=exec`, `ExecStart=/usr/local/bin/drop agent run`, `EnvironmentFile=/etc/drop/agent.env`, `LoadCredential=node-secret:/etc/drop/agent.secret`, `StateDirectory=drop-agent` (0700), `UMask=0077`, `Restart=always` with `RestartSec=5` and `RestartPreventExitStatus=3`, after `network-online.target`, `nibrunnerd.service` and `drop.service`. Hardening: `NoNewPrivileges`, `ProtectSystem=strict` with `ReadWritePaths=/var/lib/nibrunner`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, kernel and cgroup protection, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`, a small capability bounding set and `SystemCallFilter=@system-service`. Install steps are at the top of the unit. Once it runs, nothing else writes `desired.json` (runbook rule).

## Tests

`internal/agent` unit tests drive the agent against a fake server (`fake_server_test.go`) and in-memory AWS: write, backup and staging; realtime events, reconnects and the ticker; server down, 401, 403, 429, 5xx and malformed documents leaving the file untouched; foreign entries refusing start and `check` without writing; host mismatch; staging, secret and server refusals keeping previous entries; tombstones; unchanged content skipping the write; the report mapping with `cron` stripped; no secret in `state.json`, reports or logs. `tests/drop_e2e.py` (`make e2e`, CI) runs the real binary on both sides with `--fake-aws` in a temporary folder.
