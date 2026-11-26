# Hosting

People use Drop on its hosted origin: one Drop server holds several accounts, signs people in by email and offers remote MCP with OAuth. It serves plain HTTP on a listen address behind a TLS-terminating proxy (a Cloudflare Tunnel during development, an AWS load balancer at launch). Plain `serve` without `--hosted` is the local single-account server on loopback, for developing the reader; Drop has no native public HTTPS mode.

Drop 0.1.0 uses hosted mode from pocketcontext-core 0.3.1 ("Multiple hosted accounts" in its README). Drop sets `Policy.Accounts` to `AccountsMulti`, its request budget, a 30-day session and the sign-in email in `internal/droppolicy`.

## Accounts

- **The owner** is `DROP_OWNER_EMAIL`. `--hosted-init` creates a new database with the owner as its only account; an ordinary start refuses a missing database, a database whose accounts lack the owner (an owner change needs explicit maintenance) and a local database (one holding the local account). The owner is always an admin (made so again at every start).
- **Upgrading a development database to 0.1.0.** Back up the project before starting the new binary. If the database holds multiple accounts, sign in as the original owner before upgrading and record the `id` from `GET /api/drop/me`. Supply that ID as `DROP_OWNER_ID` alongside `DROP_OWNER_EMAIL` on the first upgraded start. Core records the binding in the database. You can then remove `DROP_OWNER_ID` from the environment. A database containing only its owner binds automatically.
- **Invite-only.** Other accounts exist only when `Hooks.AllowSignIn` admits an address (`internal/dropserver/accounts.go`): it needs an open (unused, unrevoked) and unexpired invite. Any other address gets the same answer as an invited one, no email and no account.
- **Invites.** An admin invites an address in the reader's Users page (`#/admin/users`), with `drop invite create EMAIL` on the server host (the CLI reaches the hosted server through its private socket and acts as the owner) or through MCP (`drop_write` with `invite.create`). An invite has a role (`user` or `admin`), legacy app-count metadata for older clients and lasts 7 days (1-30). Direct invite creation returns a sign-in URL (`https://HOST/#/invite?email=…`, no credential in it) that the admin shares. Approving a verified waitlist entry through the Waitlist page queues a welcome email automatically. The invitee signs in there with the ordinary sign-in code email; that first request creates the account with the invite's role and uses the invite up. If they never complete the sign-in, core prunes the account after 15 minutes and the invite opens again.
- **Roles.** Every account is an `admin` or a `user` (one OAuth scope, `drop`). Admins invite, revoke invites and change accounts (`user.set`: role, quota, suspension); nobody changes their own role or suspension, or the owner's. Every call is checked on the server ([Operations](ops.md)).
- **Suspension** ends the account's sessions at once and refuses its sign-ins; an account that has signed in is never deleted (`pb_hooks/account_access.js`).
- Addresses are canonical (trimmed, lower-cased ASCII); case variants are one account.
- Each account sees only its own records: the collection rules filter by owner (admins also read invites and every event), and record writes are not public routes.
- Hosted accounts all carry the same name, so the reader shows email addresses.

## Sign-in

Visitors can also join the waitlist from the sign-in form or its inbox screen. After the visitor accepts the Waitlist Terms, an unapproved address receives a six-digit verification code. A correct code confirms the waitlist entry and preserves the original join date on repeat verification. An invited or existing user receives the usual sign-in email. During deployment, Drop checks the address before sending a code and immediately reports whether it has access. Declining the waitlist stores no entry. First activation still requires acceptance of the Beta service terms.

The reader asks for an email address and sends a sign-in email with a six-digit code and a link. The code is bound to the browser or home-screen app that asked for it, works for 15 minutes and once (shared with the link), and five wrong codes end the request. The link (`/#/login?otpId=…&otp=…`) asks to confirm before it signs in, so a mail scanner cannot spend it. Every request answers the same way whether or not an email was sent, takes at least 250 ms, and is rate-limited per client address, per address and per instance. Password sign-in, `/bundle/login` and `/api/bundle/redeem` answer 404 on the hosted origin.

## What is public

The hosted origin answers only the allowlist in `internal/droppolicy/routes.go` (golden table `internal/droppolicy/testdata/public_routes.json`): the reader, `POST /api/drop/op` and `GET /api/drop/me` ([Operations](ops.md)), `PUT /api/drop/upload/<token>` for a 32-character token (one-time release uploads, anonymous but scoped to one link; [Operations](ops.md#releases)), record reads of `apps`, `invites` and `events` (their rules decide who sees what), realtime and session renewal, the node agent's routes (`POST /api/collections/nodes/auth-with-password` and `auth-refresh`, `GET /api/drop/agent/desired`, `POST /api/drop/agent/report`; node tokens only, [Agent API](agent-api.md)), plus the routes core adds: the sign-in email routes, `/up` (for health checks, on any Host) and, with `--mcp`, `/mcp`, `/oauth/*` and `/.well-known/*`. Everything else answers 404, including the SQL and search reads (`/api/context/*`, which read whole tables and stay local), every record write, `/api/batch`, `/api/files/*`, `/_/` and all local administration. Requests for any Host other than `--domain` are refused.

## Configuration

The service reads these variables from its environment (never from the project):

| Variable | Value |
| --- | --- |
| `DROP_OWNER_EMAIL` | the owner's plain address |
| `DROP_OWNER_ID` | original owner’s account ID on the first 0.1.0 start of an existing multi-account database |
| `DROP_SMTP_FROM` | the sender address of sign-in emails |
| `DROP_SMTP_HOST`, `DROP_SMTP_PORT` | the SMTP server (Resend: `smtp.resend.com`, 465) |
| `DROP_SMTP_USERNAME`, `DROP_SMTP_PASSWORD` | SMTP credentials (Resend: `resend` and a send-only API key) |
| `DROP_SMTP_TLS` | `tls` (default, implicit TLS) or `starttls` |
| `DROP_APP_DOMAIN` | optional: the domain of app hostnames (`<slug>.<DROP_APP_DOMAIN>`, for example `bigconfig.space`), set on each app at creation; without it new apps get no hostname |
| `DROP_RELEASE_MAX_BYTES` | optional: a lower release cap in bytes (1048576 up to the default and maximum, 94371840 = 90 MiB) |
| `DROP_AWS_REGION`, `DROP_S3_BUCKET`, `DROP_SSM_PREFIX` | where releases (S3) and secrets (SSM) live, through the default AWS credential chain ([aws.md](aws.md)); `--fake-aws DIR` replaces them with a local directory for testing, only with a loopback `--hosted-http-address` |
| `DROP_CREW_REPLICA_BUCKET` | the replica bucket of managed Crew apps (each app under `<app id>/`); also the bucket whose per-app request-metrics filters metering manages |
| `DROP_USAGE_LOG_BUCKET` | optional: the bucket the replica bucket delivers its S3 server access logs and inventory to; turns [usage metering](#usage-metering) on |
| `DROP_USAGE_LOG_PREFIX` | with `DROP_USAGE_LOG_BUCKET`: the access-log partition root, ending with `/`, before `YYYY/MM/DD/` (`access-logs/ACCOUNT/REGION/REPLICA_BUCKET/`) |
| `DROP_USAGE_INVENTORY_PREFIX` | optional: the inventory configuration's folder, ending with `/` (`inventory/REPLICA_BUCKET/CONFIGURATION_ID/`); without it stored bytes are not metered |

Keep the password in a root-owned environment file read by the service manager. The settings change only the server's memory; they are never saved into the database.

## Serve

```sh
drop serve --hosted --headless --mcp --domain drop.example.com --db /var/lib/drop/project/drop.db \
  --hosted-http-address 127.0.0.1:8100 \
  --hosted-proxy-header CF-Connecting-IP --hosted-proxy-networks 127.0.0.0/8
```

- `--hosted` requires `--headless`, `--domain HOST` and `--mcp`, and excludes `--ui-dev`, `--tcp-only` and `--port`. Every other hosted option requires `--hosted`.
- `--hosted-init` creates the database on the first start; leave it out afterwards. `--hosted-restore` (excluding `--hosted-init`) invalidates copied browser, email and MCP credentials before serving a deliberately restored backup; Drop discards unused upload links and rotates every node's token key (`Hooks.OnHostedRestore`, `internal/dropserver/restore.go`), so each agent gets a 401 and signs in again with its password. Restore also clears the backup’s pending SSM deletions. A parameter may have been recreated since the backup. Inspect `SELECT parameter FROM drop_secret_deletions` in a read-only copy of the backup and reconcile its entries with the restored app declarations and the current parameter store before removing any orphaned parameter. Rotate the node passwords too (`drop node rotate-secret`) if the backup may have been copied.
- `--hosted-http-address IP:PORT` is the plain HTTP listener, a literal IP and a port (default `:80`). Bind it to loopback when the proxy runs on the same host.
- `--hosted-proxy-header` and `--hosted-proxy-networks` go together. The header is `CF-Connecting-IP` (Cloudflare Tunnel) or `X-Forwarded-For` (a load balancer; the rightmost entry is used), and the networks are the comma-separated CIDR prefixes the proxy connects from. Only then does Drop key its per-client limits on the real client; without them every request counts as the proxy's address. A prefix that would trust every peer is refused.
- Hosted mode keeps only a private local socket (for `stop`, `status`, the local key and the owner's account commands `invite`, `user` and `op`), never a loopback TCP port, so the CLI's app commands (`add`, `list`, `sql` and the others) do not run against a hosted server.
- The plain HTTP server's write timeout is 5 minutes, so long-lived streams (realtime) reconnect; idle connections close after 120 s.

### Release uploads through the proxy

Releases reach Drop as one `PUT /api/drop/upload/<token>` whose body is the executable, up to 90 MiB (`dropapi.MaxReleaseBytes`; decision 2). The proxy in front must pass that request whole:

- **Body size.** Allow request bodies of at least 92 MiB (96468992 bytes) on `PUT /api/drop/upload/*`: the 90 MiB cap plus headroom. Drop itself accepts public bodies up to 92 MiB (`Policy.PublicBodyLimit`) and answers 413 `release_too_large` above the release cap. Cloudflare (tunnel or proxied DNS) limits request bodies to 100 MB on the Free and Pro plans, which is why the cap is 90 MiB; never raise `DROP_RELEASE_MAX_BYTES` above it (Drop refuses to). An AWS Application Load Balancer has no request body limit. nginx needs `client_max_body_size 92m;` (its default is 1 MiB), and Caddy `request_body { max_size 92MB }` if a limit is set.
- **Time.** The upload must finish within the server's 5-minute read timeout (about 2.5 Mbit/s for a full 90 MiB release); keep the proxy's own request and idle timeouts at least as long (an ALB's idle timeout defaults to 60 s, which a steady upload never hits).
- **Path and method.** A path-filtering proxy must pass `PUT` to `/api/drop/upload/*` with the request unbuffered or buffered whole; do not strip `Expect: 100-continue` (curl sends it, and Drop answers a refused link before reading the body).
- **Logs.** The token in the path is a credential until the link is used or expires (15 minutes). Drop logs the route as `/api/drop/upload/[redacted]`; keep the proxy's access log from recording the path too (or keep that log private).


### Development: Cloudflare Tunnel

Drop's development instance runs behind the existing `crew-dev` tunnel: an ingress rule `drop-dev.bigconfig.space → http://127.0.0.1:8100` before the catch-all, and proxied `drop-dev` and `*.bigconfig.space` DNS records. Cloudflare limits request bodies to 100 MB, which is why releases are capped at 90 MiB.

```sh
drop serve --hosted --headless --mcp --domain drop-dev.bigconfig.space --db /var/lib/drop/project/drop.db \
  --hosted-http-address 127.0.0.1:8100 --hosted-proxy-header CF-Connecting-IP --hosted-proxy-networks 127.0.0.0/8
```

### Launch: load balancer

Behind an AWS Application Load Balancer, listen on `0.0.0.0:8100`, use `--hosted-proxy-header X-Forwarded-For` and list the load balancer's subnets in `--hosted-proxy-networks`, and restrict the security group to the load balancer.

## Check

1. `curl -fsS https://HOST/up` answers 200.
2. `curl -s -o /dev/null -w '%{http_code}' https://HOST/api/context/schema` answers 404.
3. `https://HOST/.well-known/oauth-protected-resource` names `https://HOST/mcp`, and an anonymous `POST https://HOST/mcp` answers 401.
4. Sign in as the owner in a browser with the emailed code; the Users link appears.
5. On the host, `drop --db /var/lib/drop/project/drop.db invite list` answers `{"invites": [], …}` (run as the service user, so it finds the server's private socket).
6. A release upload passes the proxy: create a test app, then `drop op release.upload_link '{"app":"SLUG"}'` and `curl -fT ./hello URL` with a linux/amd64 executable (build `tests/fixtures/hello`) answers 200 with the release; an 80 MiB executable must pass as well.

## Usage metering

With `DROP_USAGE_LOG_BUCKET` and `DROP_USAGE_LOG_PREFIX` set, `drop serve` runs the usage collector a minute after it starts and then every 15 minutes; `drop usage collect` (as the owner on the server host, through the private socket) runs it once. Each run:

1. lists the access-log objects of the last three UTC days (`<DROP_USAGE_LOG_PREFIX>YYYY/MM/DD/`; S3 delivers logs late and out of order, which is why the cursor is the set of processed keys), reads each object not yet processed with a strict parser (malformed lines are counted and skipped) and adds its records to `app_usage_hourly` together with the object's cursor row in one transaction, so a crash or restart never counts an object twice. Only keys whose first segment is a Drop app id count; requests S3 refused (401, 403) count only as denied. An object S3 delivers more than two days late is not read;
2. reads the newest inventory report under `DROP_USAGE_INVENTORY_PREFIX` once into `app_usage_daily`;
3. reconciles the replica bucket's per-app request-metrics filters (CloudWatch `AWS/S3` request metrics, filter id = app id), when `DROP_CREW_REPLICA_BUCKET` is set;
4. checks every app's month against its limits ([Operations](ops.md#usage)): soft alerts are events; a hard limit also stops the app once.

The server reads only the log bucket (`s3:ListBucket`, `s3:GetObject`) and changes only the replica bucket's metrics configurations (`s3:PutMetricsConfiguration`, which also authorizes their deletion, `s3:GetMetricsConfiguration`, `s3:ListBucketMetricsConfigurations`); it never reads replica objects. It stores counts only: no requester, address or request id from a log reaches the database or the server log. In a test, `--fake-aws DIR` reads logs from `DIR/usage/logs/<key>` and keeps filters in `DIR/usage/metrics/` (still with the variables above).

### drop-ax41

The stack `drop-ax41-workloads` (`deploy/drop-ax41/workload-identity.yml`) delivers the replica bucket's partitioned access logs and a daily all-versions CSV inventory to the log bucket, and `access-drop-ax41` (`access.yml`) gives the server's IAM user the metering policy. Add to the server's environment (`/etc/drop/server.env`, beside `DROP_CREW_REPLICA_BUCKET=pocketcontext-drop-ax41-replicas-224391044680`):

```sh
DROP_USAGE_LOG_BUCKET=pocketcontext-drop-ax41-logs-224391044680
DROP_USAGE_LOG_PREFIX=access-logs/224391044680/eu-north-1/pocketcontext-drop-ax41-replicas-224391044680/
DROP_USAGE_INVENTORY_PREFIX=inventory/pocketcontext-drop-ax41-replicas-224391044680/daily-all-versions/
```

`DROP_AWS_REGION=eu-north-1` (in `/etc/drop/aws.env`) is the region of both buckets. Restart the server, then check:

1. The server log says `usage metering on`.
2. `drop --db /var/lib/drop/project/drop.db usage collect` answers `objects_listed` > 0 and `errors: []` (an `AccessDenied` error names the missing permission). `filters_created` counts the Crew apps that had no filter yet; `aws s3api list-bucket-metrics-configurations --bucket pocketcontext-drop-ax41-replicas-224391044680` lists one per Crew app.
3. `drop ... usage show SLUG` shows the app's last hours. The first inventory appears the day after the configuration was created.

## Remote MCP

See [MCP](mcp.md).

### Set up workload identity once per node

The host S3 proxy signs two-minute RS256 assertions in memory and exchanges them for an app-scoped STS session for authorized requests. At startup the Drop agent removes legacy role and token files from its managed app directories. Previously issued STS sessions retain their AWS expiry.

Use the operator's AWS profile and region. From the repository root, deploy the stack in two stages. IAM validates the published discovery document when creating the provider.

1. Run `aws cloudformation deploy --stack-name drop-workloads --template-file deploy/walter-aws/workload-identity.yml --capabilities CAPABILITY_IAM`. Obtain IssuerURL, IssuerBucket and ReplicaBucket from the stack outputs.
2. On the node, create a root-only directory and a new PKCS8 RSA signing key. Keep the private key on the node. Use a fresh filename when rotating keys.

   ```sh
   sudo install -d -m 0700 /etc/drop/workload-identity /etc/nibrunner/workload-identity
   sudo sh -ec 'umask 077; test ! -e /etc/drop/workload-identity/key.pem; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out /etc/drop/workload-identity/key.pem'
   sudo /usr/bin/python3 deploy/walter-aws/publish-issuer.py "$ISSUER_URL" "$PUBLIC_DIR" /etc/drop/workload-identity/key.pem
   ```

3. Upload the two public documents with the operator profile. The export utility uses Python cryptography on the operator host. The agent signs in Go.

   ```sh
   aws s3 cp "$PUBLIC_DIR/keys.json" "s3://$ISSUER_BUCKET/keys.json" --content-type application/json --cache-control 'public,max-age=60'
   aws s3 cp "$PUBLIC_DIR/openid-configuration" "s3://$ISSUER_BUCKET/.well-known/openid-configuration" --content-type application/json --cache-control 'public,max-age=60'
   curl -fsS "$ISSUER_URL/keys.json" | cmp - "$PUBLIC_DIR/keys.json"
   curl -fsS "$ISSUER_URL/.well-known/openid-configuration" | cmp - "$PUBLIC_DIR/openid-configuration"
   aws cloudformation deploy --stack-name drop-workloads --template-file deploy/walter-aws/workload-identity.yml --capabilities CAPABILITY_IAM --parameter-overrides EnableRole=true
   ```

4. Configure the agent environment with `DROP_IDENTITY_ISSUER` (IssuerURL), `DROP_IDENTITY_ROLE_ARN` (SharedRole) and `DROP_IDENTITY_KEY_FILE` (the private key path). The default audience is `sts.amazonaws.com`. Install the release’s agent and host-services units and enable the node’s S3 proxy. Set `DROP_CREW_REPLICA_BUCKET` to ReplicaBucket and `DROP_AWS_REGION` to its region on the Drop server. Reload systemd and restart the affected services. The release storage bucket and replica bucket serve different purposes.
5. The proxy exchanges the assertion for a shared-role session and signs requests on the host. The signed subject and app_id principal tag identify the same app. The role allows ListBucket with an app-prefixed request and object operations under that prefix. Confirm access to its own prefix and denied access to the root and another prefix.
6. For signing-key rotation export both the old and new keys in one invocation. Publish that combined JWKS, verify the public document and allow cache propagation. Switch the proxy’s key path and restart its service. Verify STS accepts the new key, then wait until every old assertion has expired before publishing a JWKS containing only the new key.

## Eligible nodes for Beta

Set `DROP_BETA_NODES` on the control plane to a comma-separated list of registered node names, in placement preference order. The default is empty and disables automatic placement. Only include hosts eligible for invited-customer workloads with the required Crew services. The dedicated drop-ax41 host with active SMT is restricted to the owner's workloads and must stay off this list while that restriction applies. For an owner-only host, set `DROP_BETA_OWNER_NODES` instead. Only apps owned by the instance's bound owner account can use this pool. Each node must belong to at most one pool. Drop checks the node's current active/online/ready status and known allocatable CPU and memory inside the placement transaction. An unavailable host produces a retryable capacity error and preserves the app and uploaded release.

Before enabling new Beta account activation, publish the owner-approved Terms of Service and Privacy Policy at distinct HTTPS URLs. Set `DROP_TERMS_URL`, `DROP_PRIVACY_URL` and `DROP_TERMS_VERSION` on the server and restart it. The hosted reader obtains all three from the public auth-mode response each time it opens. The same reader build works with a new approved version. The PocketContext-operated Beta ships the approved version `2026-11-26` at `/#/legal/2026-11-26/terms` and `/#/legal/2026-11-26/privacy` on its own HTTPS origin. These pages open before sign-in. Use a new immutable version for a changed document. An incomplete configuration closes new invited-account activation on a Beta-enabled instance. Returning verified accounts, the instance owner, and instances with neither a catalog nor configured Beta nodes retain their sign-in flow.

The manually dispatched **Drop Beta KVM acceptance** workflow runs on a disposable GitHub-hosted Linux runner. It feeds the published Crew v0.45.4-drop.5 executable into the released NibRunner v2026.11.19 inspector through Drop's admission helper, restarts the temporary Drop server, and checks that the cached manifest works with the inspector removed. Desktop and mobile Chromium clients activate separate invited accounts through a loopback SMTP mailbox, upload the released binary and resume a deployment when no node has capacity. The final check revokes the digest and verifies that a warm cache still denies it. The run uses fake AWS storage and synthetic credentials.

### Replicating the control-plane database on AX41

The optional host service in `deploy/drop-ax41/drop-control-litestream.service` runs Litestream 0.5.18 as the Drop service user. It follows PocketContext Core's private working database while Drop serves. The project database is periodically replaced by atomic snapshot and is unsuitable as the replication source.

Deploy the updated `deploy/drop-ax41/access.yml` to the existing `access-drop-ax41` stack with its admin credentials and existing parameters. Its retained, encrypted and versioned control-plane bucket is separate from the app replica bucket. Only the server receives the new object permissions. Install the upstream Litestream 0.5.18 Linux amd64 executable as `/usr/local/bin/litestream` (executable SHA-256 `f2d452c1527be81f06d5f135655b81b0e8d9a338cb4c2825cf2243134bc2dbd6`). Install the shipped YAML as `/etc/drop/control-litestream.yml` and the unit under `/etc/systemd/system/`. Set the following in root-owned `/etc/drop/control-replica.env`, mode 0600, substituting the actual account and verified private working path for the running project.

```ini
AWS_REGION=eu-north-1
DROP_CONTROL_LIVE_DB=/var/lib/drop/.cache/pocketcontext-drop/4dc82c906e280ddd/live/data.db
DROP_CONTROL_REPLICA_URL=s3://pocketcontext-drop-control-ax41-ACCOUNT/control
```

Run `systemctl daemon-reload` and `systemctl enable --now drop-control-litestream.service`. For a scratch restore, run the following as the service user with the same environment as the replication unit. The restored database contains private account data. Keep the scratch directory private and remove it after the drill.

```sh
set -eu
umask 077
scratch=$(mktemp -d)
litestream sync -wait -socket "$DROP_REPLICA_SOCKET" "$DROP_CONTROL_LIVE_DB"
litestream restore -config /etc/drop/control-litestream.yml -o "$scratch/drop.db" "$DROP_CONTROL_LIVE_DB"
sqlite3 -readonly "$scratch/drop.db" 'PRAGMA integrity_check'
```

For disaster recovery, fence the old Drop writer and its Litestream service. Restore into an empty project with a fresh private cache, verify integrity, and follow the hosted restore procedure above, including SSM reconciliation and a first start with `--hosted-restore`. Before enabling replication on the recovered instance, configure the new working path and a fresh replica prefix under `control/` to preserve the previous history.

Run `deploy/drop-ax41/test-control-replica.py` after `make build` with Python, `aiosmtpd`, Moto's server extra and Litestream 0.5.18 on PATH to exercise the same configuration against loopback S3.

### Private Beta S3 pricing

The S3 collector settles billable usage within the transaction that records each access-log object or inventory. Storage includes current and noncurrent bytes, priced per UTC inventory day using the number of days in that calendar month. S3 Standard Stockholm inputs are USD 0.005 per thousand class A requests, 0.0004 per thousand class B, 0.023 per GiB-month and 0.09 per GiB of outbound data. The server applies the configured USD-to-EUR rate and markup, carries sub-microcredit fractions, and limits deductions to remaining funds. The source for request and storage rates is the [AWS eu-north-1 S3 offer](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonS3/current/eu-north-1/index.json).
