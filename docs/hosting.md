# Hosting

People use Drop on its hosted origin: one Drop server holds several accounts, signs people in by email and offers remote MCP with OAuth. It serves plain HTTP on a listen address behind a TLS-terminating proxy (a Cloudflare Tunnel during development, an AWS load balancer at launch). Plain `serve` without `--hosted` is the local single-account server on loopback, for developing the reader; Drop has no native public HTTPS mode.

Hosted mode comes from pocketcontext-core 0.3.0 ("Multiple hosted accounts" in its README). Drop sets `Policy.Accounts` to `AccountsMulti`, its request budget, a 30-day session and the sign-in email in `internal/droppolicy`.

## Accounts

- **The owner** is `DROP_OWNER_EMAIL`. `--hosted-init` creates a new database with the owner as its only account; an ordinary start refuses a missing database, a database whose accounts lack the owner (an owner change needs explicit maintenance) and a local database (one holding the local account).
- **Other accounts** exist only when `Hooks.AllowSignIn` admits an address (`internal/dropserver/hooks.go`). Until invites arrive it admits nobody, so only the owner can sign in; any other address gets the same answer and no email.
- Addresses are canonical (trimmed, lower-cased ASCII); case variants are one account. An account that never signed in is unverified and pruned by core after 15 minutes; one that has signed in is never deleted (`pb_hooks/account_access.js`).
- Each account sees only its own records: the collection rules filter by owner, and record writes are not public routes.
- Hosted accounts all carry the same name, so the reader shows email addresses.

## Sign-in

The reader asks for an email address and sends a sign-in email with a six-digit code and a link. The code is bound to the browser or home-screen app that asked for it, works for 15 minutes and once (shared with the link), and five wrong codes end the request. The link (`/#/login?otpId=…&otp=…`) asks to confirm before it signs in, so a mail scanner cannot spend it. Every request answers the same way whether or not an email was sent, takes at least 250 ms, and is rate-limited per client address, per address and per instance. Password sign-in, `/bundle/login` and `/api/bundle/redeem` answer 404 on the hosted origin.

## What is public

The hosted origin answers only the allowlist in `internal/droppolicy/routes.go` (golden table `internal/droppolicy/testdata/public_routes.json`), plus the routes core adds: the sign-in email routes, `/up` (for health checks, on any Host) and, with `--mcp`, `/mcp`, `/oauth/*` and `/.well-known/*`. Everything else answers 404, including the SQL and search reads (`/api/context/*`, which read whole tables and stay local), every record write, `/api/batch`, `/api/files/*`, `/_/` and all local administration. Requests for any Host other than `--domain` are refused.

## Configuration

The service reads these variables from its environment (never from the project):

| Variable | Value |
| --- | --- |
| `DROP_OWNER_EMAIL` | the owner's plain address |
| `DROP_SMTP_FROM` | the sender address of sign-in emails |
| `DROP_SMTP_HOST`, `DROP_SMTP_PORT` | the SMTP server (Resend: `smtp.resend.com`, 465) |
| `DROP_SMTP_USERNAME`, `DROP_SMTP_PASSWORD` | SMTP credentials (Resend: `resend` and a send-only API key) |
| `DROP_SMTP_TLS` | `tls` (default, implicit TLS) or `starttls` |
| `DROP_AWS_REGION`, `DROP_S3_BUCKET`, `DROP_SSM_PREFIX` | where releases (S3) and secrets (SSM) live, through the default AWS credential chain ([aws.md](aws.md)); `--fake-aws DIR` replaces them with a local directory for testing, only with a loopback `--hosted-http-address` |

Keep the password in a root-owned environment file read by the service manager. The settings change only the server's memory; they are never saved into the database.

## Serve

```sh
drop serve --hosted --headless --mcp --domain drop.example.com --db /var/lib/drop/project/drop.db \
  --hosted-http-address 127.0.0.1:8100 \
  --hosted-proxy-header CF-Connecting-IP --hosted-proxy-networks 127.0.0.0/8
```

- `--hosted` requires `--headless`, `--domain HOST` and `--mcp`, and excludes `--ui-dev`, `--tcp-only` and `--port`. Every other hosted option requires `--hosted`.
- `--hosted-init` creates the database on the first start; leave it out afterwards. `--hosted-restore` (excluding `--hosted-init`) invalidates copied browser, email and MCP credentials before serving a deliberately restored backup.
- `--hosted-http-address IP:PORT` is the plain HTTP listener, a literal IP and a port (default `:80`). Bind it to loopback when the proxy runs on the same host.
- `--hosted-proxy-header` and `--hosted-proxy-networks` go together. The header is `CF-Connecting-IP` (Cloudflare Tunnel) or `X-Forwarded-For` (a load balancer; the rightmost entry is used), and the networks are the comma-separated CIDR prefixes the proxy connects from. Only then does Drop key its per-client limits on the real client; without them every request counts as the proxy's address. A prefix that would trust every peer is refused.
- Hosted mode keeps only a private local socket (for `stop`, `status` and the local key), never a loopback TCP port, so the CLI's data commands (`add`, `list`, `sql` and the others) do not run against a hosted server.
- The plain HTTP server's write timeout is 5 minutes, so long-lived streams (realtime) reconnect; idle connections close after 120 s.

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
4. Sign in as the owner in a browser with the emailed code.

## Remote MCP

See [MCP](mcp.md).
