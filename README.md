# Drop

PocketContext Drop: a private, invite-only service that runs people's apps (one Linux executable each) on a PocketContext-managed host. It builds on the shared library [pocketcontext-core](https://github.com/pocketcontext-labs/pocketcontext-core), pinned to an exact release in `go.mod`, and is one Go executable with nothing else needed at run time: a server on PocketBase with hosted multi-account mode (email sign-in, remote MCP with OAuth) behind a TLS-terminating proxy, a browser reader, a command-line client and an agent skill for Claude Code, Codex and Antigravity.

**[Drop 0.11.0](docs/releases/0.11.0.md)** makes every node equal: each node's own Cloudflare Tunnel, per-app DNS records kept by the control plane, and the placement pool as a field on the node record. Previous: [Drop 0.10.0](docs/releases/0.10.0.md).

**[Drop 0.9.0](docs/releases/0.9.0.md)** hosts official Crew and Atlas apps, with owner-selected upgrades, a shared awake-time buffer and funded storage enforcement.

**[Drop 0.8.0](docs/releases/0.8.0.md)** adds shareable deployment links, verified archive imports, PocketBase and ZeroClaw recipes, an email-first login, and deletion receipts.

**[Drop 0.7.0](docs/releases/0.7.0.md)** adds deploy-time invitation checks, manifest and generic binary deployment, a shared awake-time trial, compute and S3 credit accounting, and owner-confirmed permanent deletion.

**[Drop 0.6.2](docs/releases/0.6.2.md)** lets visitors join the waitlist from the email sign-in flow. They review the waitlist terms and verify their address with a six-digit code.

**[Drop 0.6.1](docs/releases/0.6.1.md)** improves the Nodes and Users admin pages. Long host and account details fit narrow screens, and related controls align across rows.

**[Drop 0.6.0](docs/releases/0.6.0.md)** adds a verified email waitlist and separates hosted account administration from app deployment. Administrators manage access, and invited users deploy their own apps.

**[Drop 0.5.1](docs/releases/0.5.1.md)** lets Crew owners choose any size from 1× to 8×, suggests 4× for the current Crew release and shows the assigned HTTPS address with a copy button.

**[Drop 0.5.0](docs/releases/0.5.0.md)** adds the invite-only Beta deployment flow, a digest-based manifest cache and automatic placement on eligible hosts. See the [Beta release admission guide](docs/ops.md#beta-release-admission-and-deployment).

**[Drop 0.4.0](docs/releases/0.4.0.md)** keeps AWS credentials out of managed Crew guests: a node S3 proxy signs again with the app's own session and enforces limits in real time. See [host services](docs/agent.md#host-services).

**[Drop 0.3.0](docs/releases/0.3.0.md)** meters each app's S3 use from the replica bucket's access logs and inventory, with monthly soft and hard limits, a Usage tab and a Crew replica sync interval setting. See [usage metering](docs/hosting.md#usage-metering).

**[Drop 0.2.0](docs/releases/0.2.0.md)** adds private lifecycle hooks and signed workload identities for hosted Crew apps. See the [node setup](docs/hosting.md) before enabling Crew on a host.

**[Drop 0.1.0](docs/releases/0.1.0.md)** is the first release. Derived from the PocketContext family template (see [origin](docs/origin.md)), Drop serves people in hosted mode on pocketcontext-core 0.3.1: several accounts (`AccountsMulti`), email sign-in with a six-digit code, remote MCP with OAuth and a default-deny route allowlist, behind a Cloudflare Tunnel or load balancer ([Hosting](docs/hosting.md)). Drop is invite-only: every account is an admin or a user, and admins invite people by email address (the invite returns a sign-in link to share; no invite email is sent). Account operations go through one route, `POST /api/drop/op` ([Operations](docs/ops.md)), which checks the account and its role and records an event. Apps have settings (state, resources, port, executable, arguments, environment, health check), a per-user quota (3 by default) and a deployment number that goes up whenever what the app runs with changes; their secrets live only in AWS SSM, written by a server that cannot read them back. Releases are static linux/amd64 executables of at most 90 MiB, uploaded through one-time links (`drop release upload`, the reader's Releases tab with drag and drop, or `curl -fT FILE URL`) and stored once by content in S3; deploying one starts a new deployment. Assistants connect through remote MCP with two tools, `drop_read` and `drop_write`, over the same operations ([MCP](docs/mcp.md)). Admins add nodes (the hosts, each with one agent that signs in as its node) and place apps on them; the agent pulls its node's whole desired document and reports each app's phase, which people see on the app's Overview ([Agent API](docs/agent-api.md)). The host agent, `drop agent run` (root, `deploy/walter-aws/drop-agent.service`), follows that document, stages releases by sha256, reads secret versions from SSM and writes nibrunner's whole `desired.json` atomically; it preserves the existing `desired.json` when the server is unreachable and refuses to start while the file holds entries Drop did not create ([Node agent](docs/agent.md)). A local project (one account, loopback only) remains for developing the reader.

**Linux only** (amd64 and arm64). The server and its host agent run only on Linux, and people need only a browser, remote MCP and `curl` for uploads; a build for any other system refuses to run (`cmd/drop/main_other.go`). A future laptop client would be a separate pure-Go program.

## A project

```
Drop/          ← any folder; may be in a synced folder
  drop         the binary (optional here; it can live anywhere)
  drop.db      the database
  drop.json    port and project ID (no secrets)
  storage/     PocketBase file storage (unused by apps)
```

The database defaults to `drop.db` beside the binary; `--db PATH` or `DROP_DB` selects another. While the server runs, a working copy lives in the per-machine cache (`os.UserCacheDir()/pocketcontext-drop/<key>/`) together with the log and the private local key; unpacked application files live in `os.UserCacheDir()/pocketcontext-drop/app/`. The server writes a consistent copy (`VACUUM INTO` plus an atomic rename) back to `drop.db` a few seconds after changes and on `stop`, so sync clients only ever see complete files.

A database is opened only by a release that ships every schema migration it has applied. Any other database (one of a newer release or of another product) is refused on start before any migration runs and left unchanged.

## Quick start

Build first (see [Build](#build)), then:

```sh
bin/drop init                    # asks for a reader password
bin/drop open                    # reader, signed in
bin/drop app create "Hello app" --slug hello-app
bin/drop app list
bin/drop search "hello"
bin/drop sql "SELECT id, name, slug, desired_state, created FROM apps ORDER BY created DESC LIMIT 20"
bin/drop invite create friend@example.com   # the local account is an admin
bin/drop user list
bin/drop skill                   # Claude Code, Codex, Antigravity
bin/drop stop
```

Without `--db`, the database is `bin/drop.db`. The server listens on `http://127.0.0.1:8100` (change `port` in `drop.json`, or `init --port N`). Output is JSON; `--pretty` indents it. Run `drop help` for every command and `drop help COMMAND` for one.

`skill` installs `skills/drop` with this computer's absolute binary path and database into `~/.claude/skills/drop`, `~/.codex/skills/drop` and `~/.gemini/antigravity/skills/drop`, skipping agents whose configuration folder is missing (`--agent` selects one, `--dir` a custom folder). With `--url` it installs the remote variant, `SKILL.remote.md`.

## Authentication

**Hosted** (`serve --hosted`): the owner (`DROP_OWNER_EMAIL`) and invited people sign in with their email address: the reader sends a sign-in email with a six-digit code and a link, and sessions last 30 days, renewed while the reader is in use. Each account sees only its own records; only an address with an open invite gets an account, with the invite's role (`admin` or `user`). Password sign-in, single-use links and the local routes are not on the hosted origin. See [Hosting](docs/hosting.md).

**Local** (plain `serve`, for developing the reader): a project has exactly one account, created by `init`, with no name or email to choose (`users` holds one hidden record). The reader asks only for its password. `open` signs the browser in with a single-use link valid for two minutes; `login-code` issues one-time codes for remote clients (see below). The CLI needs no password: it reads a random key that the server writes to the private cache at start and exchanges it, in the `X-Drop-Local-Key` header, for an ordinary `users` token. Anyone who can read that key can already read the database. All APIs, including SQL and search, still require a token, CORS allows only the server's own origin, and requests for any other `Host` are refused, so web pages cannot reach the server through `127.0.0.1`. The local server listens on loopback only. `/api/bundle/instance` answers `{"app": "drop", ...}` so the CLI and the launcher can tell this server from another program on the port.

### Official app catalog

After the admin stages and publishes checked Crew and Atlas releases and
enables the official catalog, customers create named apps from the
catalog, choose a 1×–8× resource size, and install new releases individually.
Developers can use the same form with a custom Linux executable from a file or public HTTPS URL. Drop inspects its manifest before creation. Custom instances receive a generated `dev-` address. Its HTTPS root opens directly and the app handles its own sign-in. When an admin publishes a new product, its catalog prefix applies to new catalog instances.
The admin **Users** page combines waiting, invited and active accounts with
search, filters and pagination. **Releases** manages official binaries and
reviewed GitHub commits and allowed upgrade sources. See [official app operations](docs/ops.md#official-crew-and-atlas-apps)
for the production cutover and upgrade procedure.

### Legacy deploy links before catalog cutover

Hosted users can open **Catalog** and choose **Deploy** beside a pinned PocketBase or ZeroClaw release. Each link opens the generic deployment form for review. Opening a link creates no app.

To link to a release from another project, point to `/deploy` on the hosted Drop origin. Include `name` and the public HTTPS release URL in `binary`. Supply the archive SHA-256 in `sha256`, the HTTP port in `port`, and the exact argument list as a single JSON array in `arg`. Encode the fields with `URLSearchParams` or an equivalent URL builder so each argument retains its original spaces and punctuation. `minimal=true` requests the compact form. The deployment still requires sign-in and owner confirmation. Deploy links are public and should contain only public release settings.

### Creating a Crew app

In hosted Drop, select **PocketContext Crew** when creating an app or pass `--kind crew` to `drop app create`. Drop copies your verified address to the new app. Crew and the node mail service use that address across restarts and moves. Configure the relay once on NibRunner. To pause delivery for a Crew app, change its sign-in email setting or run `drop app set APP --sign-in-email disabled`.

## Several computers

Only one computer may run a project at a time. `drop.db.lease` carries a heartbeat; another computer refuses to start for two minutes after the last one (`--take-over` overrides when that computer is off). Run `drop stop` before switching computers and wait for the sync client to finish.

If the synced `drop.db` changes while this computer has unsaved changes, nothing is overwritten. This computer's version is kept in `drop.db.conflict-<host>-<time>.db`, the server reports it, and the next start asks for `drop recover --keep local` (the other computer's file is saved as `drop.db.replaced-<time>.db`) or `--keep synced` (this computer's work stays in the conflict copy). Keep the folder available offline: online-only placeholders make the database unavailable.

## Remote client

The local server can run on one computer (the agent computer) while a person uses the same binary on their own computer as a client only, through an SSH forward of the same port. Sign-in uses one-time codes, so no password passes through whoever operates the agent computer.

```sh
drop login-code        # on the agent computer: prints {"code","expires"}
ssh -L 8100:127.0.0.1:8100 <agent>   # on the person's computer, in another terminal
printf '%s\n' "$CODE" | drop login --url http://127.0.0.1:8100 --code-stdin
drop app list --url http://127.0.0.1:8100
drop skill --url http://127.0.0.1:8100
```

For the browser reader, run `drop open --no-browser` on the agent computer and open the printed `signInLink` on the person's computer, or choose "Use a sign-in code instead" in the reader.

Codes and links are single use and kept only in the server's memory, so a server restart invalidates them. A `login-code` code is valid for 10 minutes (`--ttl 30`-`600` seconds); an `open` link for 2 minutes. `login --url URL` without `--code-stdin` signs in with the reader password instead, and `password` on the agent computer changes it (which also ends existing sessions).

A different local port is refused by the server's Host check. `--url` (or `DROP_URL`; an explicit `--db` overrides it) accepts only `http://127.0.0.1:PORT` or `http://localhost:PORT`. Remote mode never resolves or creates a database, never starts a server and never reads the local key. `login` stores only the token, in `os.UserCacheDir()/pocketcontext-drop/remote-<hash>/session.json` (private), and asks for a new sign-in when it expires (7 days) or when the URL serves a different project. `logout` deletes it. Server commands (`serve`, `start`, `ensure-running`, `stop`, `flush`, `recover`, `init`, `password`, `login-code`) run on the agent computer only.

## Hosted mode

```sh
drop serve --hosted --headless --mcp --domain drop.example.com [--hosted-init | --hosted-restore] \
  [--hosted-http-address IP:PORT] [--hosted-proxy-header CF-Connecting-IP|X-Forwarded-For --hosted-proxy-networks CIDR[,CIDR]]
```

Hosted mode serves plain HTTP behind a TLS-terminating proxy (default `:80`, or `--hosted-http-address`), with email sign-in configured by `DROP_OWNER_EMAIL` and `DROP_SMTP_*`, and remote MCP with OAuth at `https://HOST/mcp`. `--hosted-init` creates a new database; ordinary starts refuse a missing one. With `--hosted-proxy-header` and `--hosted-proxy-networks`, per-client sign-in limits key on the client the proxy reports. The hosted origin answers only the allowlist in `internal/droppolicy/routes.go` (golden table in `internal/droppolicy/testdata/`) and core's sign-in, `/up`, OAuth and MCP routes; SQL, search, record writes and local administration answer 404. MCP has two tools, `drop_read` and `drop_write`, which call Drop's operations with the connected account (each role sees its own enums; secrets are write-only). Without `--hosted`, `serve` stays on loopback; there is no native public HTTPS mode and no tunnel. See [Hosting](docs/hosting.md) and [MCP](docs/mcp.md).

## Data model

Hosted users deploy approved catalog releases. Developers can also deploy custom Linux executables that implement `--drop-manifest` and submit a GitHub commit for catalog review. Each account has one role. Invite each additional admin by email. Admins have no personal credits. Customers can switch between user and developer while retaining their apps and credits. Hosted admins, including the instance owner, manage accounts, catalog, nodes, placement and metering. Accounts (`users`) carry a role and a suspension flag. The historical app_quota field remains for client compatibility. Credit accounts and retained disk determine limits; `invites` hold who may sign in; `events` record every change made through an operation. They are written only by Drop's operations and account hooks. `apps` belong to the account that created them (the owner manages custom releases and settings) and `app_secret_names` record each secret's name, SSM version and length, never its value. Both are written only by the `app.*` and `secret.*` operations; the reader and the CLI (`drop app`, `drop secret`) use them, and the local CLI also reads apps through authenticated read-only SQL (`/api/context/query`) and search (`/api/context/search`), which read whole tables and so stay on the loopback server. See [docs/data-model.md](docs/data-model.md), including how to add a collection.

## Build

Requires Linux, Go (see `go.mod`) with CGO and a C compiler, Node.js 24 and pnpm 10.33.2. GNU Make runs the targets below (`nix shell nixpkgs#gnumake` where it is missing).

```sh
make ui        # pnpm install --frozen-lockfile and build the reader into ui/dist (embedded by go build)
make build     # bin/drop
make test      # Go unit tests (including the vendored server's), launcher tests, release tests
make names     # retired-name check of the repository and bin/drop (tests/name_check.py)
make identity  # template identity check of the repository and bin/drop (tests/identity_check.py)
python3 -m venv /tmp/drop-test-mail
. /tmp/drop-test-mail/bin/activate
python -m pip install aiosmtpd==1.4.6  # loopback email verification in hosted tests
make e2e       # names, identity and the anonymous route table, then CLI, remote, hosted and sync end-to-end tests with synthetic apps
```

`GOOS=darwin go build ./cmd/drop` (or any other system) builds only `cmd/drop/main_other.go`, which exits with "Drop runs on Linux only"; CI checks that no other package is linked into such a build.

### Build tags

`build-tags.txt` is the one list of Go build tags every build, test and release uses. It is copied unchanged from pocketcontext-core: SQLite features (`sqlite_math_functions`, `sqlite_percentile` and `sqlite_fts5`, the functions in the SQL reader's allowlist and the in-memory full-text search index), then the `ts_omit_` tags of core's tunnel, which Drop does not link. CI compares it with the copy in the pinned core release. A feature that Drop needs beyond these is added deliberately and extends `internal/appconfig/sqlite_test.go`, which checks that every listed tag is compiled in and that the reader still denies `dbstat`, `pragma_*` functions, `sqlite_schema`, FTS shadow tables and writes. Never add `icu`, `userauth`, `allow_uri_authority`, `secure_delete`, `vacuum_*`, `foreign_keys`, `app_armor` or `os_trace`: they weaken isolation or change storage semantics, and the test refuses them.

## Develop the reader

Build the executable once with `make ui && make build`, then use Vite hot reload while editing `ui/src`. Choose a separate development project with synthetic data. Initialize it once and stop the server that `init` starts:

```sh
bin/drop init --db /absolute/path/to/dev/drop.db --port 18100
bin/drop stop --db /absolute/path/to/dev/drop.db
node scripts/dev.mjs --db /absolute/path/to/dev/drop.db
# Equivalent: make dev DB=/absolute/path/to/dev/drop.db
```

The launcher requires an explicit existing database, reads its port from `drop.json`, starts Vite on `127.0.0.1:5173` with `DROP_DEV_PORT` set to that port, then runs the existing binary with `serve --ui-dev http://127.0.0.1:5173`. It refuses occupied ports, waits for readiness and stops its processes on Ctrl-C, using the normal authenticated shutdown to flush database snapshots. It neither installs dependencies nor rebuilds the executable. Use `--binary PATH` for another executable or `--vite-port PORT` when 5173 is occupied. Develop on Linux (for example on the development host with `--ui-dev` through an SSH port forward, or in a Linux container).

Open the reader at the project port (18100 in this example) and sign in with the development password, or run `bin/drop open --db /absolute/path/to/dev/drop.db` in another terminal. The browser stays on Drop's origin: Go serves authentication and APIs, and proxies UI modules and the hot-reload WebSocket to Vite, which is an internal upstream. `--ui-dev` is a foreground `serve` option, accepts only a loopback HTTP upstream and is never stored in project configuration. Go code, hooks and migrations still need a rebuild and restart.

Hot reload does not type-check. Run `pnpm typecheck`, `pnpm test` and `pnpm build` in `ui/` after changes, plus `node --test tests/dev_launcher.test.mjs` after launcher changes.

The browser checks use synthetic data. Build the reader and binary, then install the pinned Chromium with `(cd ui && pnpm exec playwright install chromium)`. CI runs the three checks below on Linux amd64 and arm64. Local Chromium also needs its system libraries.

- `node tests/reader_ui_browser.mjs --binary bin/drop` checks sign-in, apps, secrets, invitations, node placement and Connect at desktop and mobile widths. Add `--screenshots DIR` to keep screenshots.
- `node tests/ui_dev_browser.mjs --binary bin/drop` checks hot reload and development routing, including loopback forwards. Both Node scripts accept `--browser /path/to/chromium`. The development check also accepts `--playwright /absolute/path/to/playwright/index.mjs`.
- `DROP_TEST_BROWSER=1 go test -count=1 -tags "$(cat build-tags.txt)" -run TestTenantReader ./tests` signs two separate Chromium contexts into the hosted public router. Each opens its own app, receives a 404 for the other account’s app and is refused the admin pages.

## Tests

| Command | Checks |
| --- | --- |
| `make test` | Go unit tests with the tags in `build-tags.txt` (the route golden table and sign-in email in `internal/droppolicy`; hosted Multi startup, owner sign-in with the emailed code, invite-only sign-in and roles, the operation framework, invites, accounts, idempotency keys, the isolation suite (accounts A and B, an admin and nodes N1 and N2 across records, operations, upload, agent routes and realtime), limiters, restore and the leak scan, the public boundary, app ownership, releases and the one-time upload route in `internal/dropserver`; the frozen public route list; the operation registry against its handlers and the reader's list in `internal/dropapi` through the `internal/droptest` helpers; hooks and SQLite features in `internal/appconfig`; client and MCP (tools by role, the op table against the registry, a user forcing an admin operation, the MCP isolation matrix and the MCP secret sentinel); vendored server; `internal/leakscan`), `tests/dev_launcher.test.mjs`, `tests/test_release.py`, `tests/test_leak_scan.py` |
| `(cd ui && pnpm typecheck && pnpm test)` | reader types and unit tests (Vitest) |
| `make names` | `tests/name_check.py`: the retired product name appears nowhere in the repository or binary |
| `make identity` | `tests/identity_check.py`: no template identity (name, module, port, hello-world collection, identity fields) left in the repository or binary |
| `make e2e` | `tests/public_routes.py` (on a loopback server: the frozen anonymous route table, foreign-Host refusal and loopback-only listening; refusal of hosted options without `--hosted`, of invalid hosted options and of `open --public`) and `tests/e2e.py`: init, CLI, the single local account (an admin) and authentication on every API, invites, accounts, operations and idempotency keys, app ownership, SQL policy, search, skill, lease, conflict and recover, refusal of other releases' databases, remote mode, login codes, and a real hosted server (`--hosted-init`, the public boundary, MCP discovery, an unknown address getting no account, an invite through the private socket giving the invited address an account with its role, apps and secrets in the `--fake-aws` directory, and a release built from `tests/fixtures/hello` uploaded by the invited user through the public upload route, landing at `releases/<sha256>`, then a node and a placement driven by `tests/fake_agent.py`, the agent API contract in Python: node sign-in, realtime wake-up on `nodes/<id>`, the desired document with secrets resolved from the fake directory, reports, `app status`, password rotation and disabling), in a temporary folder; and `tests/drop_e2e.py`: the real `drop agent` against the real hosted server, both with `--fake-aws`, on temporary nibrunner paths (check, staging, secret resolution, the atomic write and `.drop-prev`, `running` from a fake `reported.json`, a realtime change, the server stopped with no write, foreign entries refusing `run` and `check` with exit 3, no secret in `state.json` or output); both need the Go toolchain to build the test release |
| `node tests/reader_ui_browser.mjs` | the embedded reader in Chromium |
| `python3 tests/archive_deploy_e2e.py --drop bin/drop --nibrunner DIR --firecracker PATH` | Opt-in Linux amd64 KVM acceptance for both archive recipes. Requires the browser test dependencies, aiosmtpd, static BusyBox, e2fsprogs and the verified NibRunner kernel and rootfs. Checks guest setup, updates and volume persistence. Pass `--archives DIR` to replay pinned downloads offline. |
| `node tests/ui_dev_browser.mjs` | hot reload through `serve --ui-dev`, and a same-port loopback forward |

CI (`.github/workflows/test.yml`) runs the reader, unit, release-script, name, identity, end-to-end and browser tests on Linux amd64 (`ubuntu-24.04`) and arm64 (`ubuntu-24.04-arm`), and checks that other systems build only the refusal.

## Releasing

`scripts/release.py` and `.github/workflows/release.yml`: pushing a `vX.Y.Z` tag on `main` builds static musl binaries natively on Linux amd64 and arm64, runs the unit, name, identity and end-to-end checks against each, assembles `drop_<version>_linux_<arch>.tar.gz`, `install.sh` (from `scripts/install.sh.in`) and `checksums.txt`, publishes an immutable GitHub release, then downloads it again with the repository token, checks it against `checksums.txt` and runs the end-to-end test on the published binaries. A manual run of the workflow builds and validates without publishing. Release notes for a version, when present, come from `docs/releases/<version>.md`.

`install.sh` downloads from the release's public URLs, and verifies the selected archive:

```sh
curl -fsSL https://github.com/pocketcontext-labs/pocketcontext-drop/releases/latest/download/install.sh | sh -s -- "$HOME/Drop"
```

To download the release archives directly, use `gh release download vX.Y.Z -R pocketcontext-labs/pocketcontext-drop` and check `sha256sum -c checksums.txt`. The installer has no default folder: name it as its argument (or `DROP_INSTALL_DIR`). It verifies the archive against `checksums.txt` and installs only the executable.

## Layout

| Path | Content |
| --- | --- |
| `internal/appconfig` | the runtime on pocketcontext-core, built from `droppolicy` and `dropserver` |
| `internal/droppolicy` | `Identity`, `Text` (with the sign-in email), `Policy` (`AccountsMulti`, budget, session, OAuth scope), the hosted route allowlist and its golden table |
| `internal/dropapi` | the operations' wire contract: request, result and error shapes and the registry of operation names, roles and inputs |
| `internal/dropserver` | hooks and accounts (`AllowSignIn`, roles, invites), the operation framework (`POST /api/drop/op`) and its operations (accounts, apps, secrets), deployments, events, idempotency keys, the services serve hands over (`services.go`), the SQL and search reads |
| `internal/appspec` | the app value rules shared by the operations and the renderer |
| `internal/render`, `internal/awsx` | the node's `desired.json` renderer; S3 and SSM clients and fakes |
| `internal/agent` | the node agent (`drop agent run`, `check` and `render`; [Node agent](docs/agent.md)) |
| `internal/droptest` | hosted test server helpers: owner and email sign-in, invited accounts, operations, OAuth, MCP |
| `internal/leakscan` | test support: the secret-leak scan over databases, files, logs and answers (`tests/leak_scan.py` is the Python twin) |
| `internal/buildinfo` | version and commit stamped at build time |
| `cmd/drop` | entry point (`main.go`, Linux) and the refusal on every other system (`main_other.go`) |
| `internal/client` | CLI commands, hosted serve options (`serve_hosted.go`), app search (`apps.go`), operations with the app, secret and account commands (`ops.go`), remote mode, skill installation, the MCP adapter and its two tools (`mcp.go`, `drop_mcp.go`), help |
| `internal/pocketcontext` | vendored PocketContext server (SQL reads, search, tracing); see `VENDORED.md`. The project, sync-safe database handling, local routes, hosted mode and UI development proxy live in [pocketcontext-core](https://github.com/pocketcontext-labs/pocketcontext-core) |
| `pb_hooks`, `pb_migrations` | schema and server-side rules |
| `server.json` | SQL column allowlist and search index configuration (local only) |
| `ui` | browser reader (React 19, Vite) |
| `embed.go` | embeds `pb_hooks`, `pb_migrations`, `server.json`, `ui/dist` and `skills/drop` |
| `skills/drop` | skill template (`SKILL.remote.md` for `skill --url`); `drop skill` fills in absolute paths |
| `scripts` | `dev.mjs` launcher, `release.py`, installer template, license notices |
| `tests` | `e2e.py`, `drop_e2e.py` (the node agent end to end), `fake_agent.py` (the agent API contract), `fixtures/hello` (the test release, built from source), `public_routes.py` and `testdata/public_routes.json`, `name_check.py`, `identity_check.py`, `leak_scan.py` (the secret-leak scan) and `test_leak_scan.py`, `test_release.py`, `dev_launcher.test.mjs`, browser checks |
| `docs` | data model, [Operations](docs/ops.md), origin, [Hosting](docs/hosting.md), [MCP](docs/mcp.md) and [Node agent](docs/agent.md) manuals |
