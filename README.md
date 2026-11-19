# Drop

PocketContext Drop: a private, invite-only service that runs people's apps (one Linux executable each) on a PocketContext-managed host. It builds on the shared library [pocketcontext-core](https://github.com/pocketcontext-labs/pocketcontext-core), pinned to an exact release in `go.mod`, and is one Go executable with nothing else needed at run time: a server on PocketBase, a browser reader, a command-line client, a private tunnel, opt-in public HTTPS and remote MCP, and an agent skill for Claude Code, Codex and Antigravity.

**Status: first scaffold.** This repository was derived from the PocketContext family template (see [origin](docs/origin.md)) and still has the template's single-account local model with a placeholder collection, `apps` (a name and a unique slug). Later milestones turn it into the hosted multi-account service: invites and roles, apps with releases and secrets, nodes and the host agent. The repository is private.

**Linux only** (amd64 and arm64). The server and its host agent run only on Linux, and people need only a browser, remote MCP and `curl` for uploads; a build for any other system refuses to run (`cmd/drop/main_other.go`). A future laptop client would be a separate pure-Go program.

## A project

```
Drop/          ← any folder; may be in a synced folder
  drop         the binary (optional here; it can live anywhere)
  drop.db      the database
  drop.json    port and project ID (no secrets)
  storage/     PocketBase file storage (unused by apps)
```

The database defaults to `drop.db` beside the binary; `--db PATH` or `DROP_DB` selects another. While the server runs, a working copy lives in the per-machine cache (`os.UserCacheDir()/pocketcontext-drop/<key>/`) together with the log, the private local key and the tunnel identity; unpacked application files live in `os.UserCacheDir()/pocketcontext-drop/app/`. The server writes a consistent copy (`VACUUM INTO` plus an atomic rename) back to `drop.db` a few seconds after changes and on `stop`, so sync clients only ever see complete files.

A database is opened only by a release that ships every schema migration it has applied. Any other database (one of a newer release or of another product) is refused on start before any migration runs and left unchanged.

## Quick start

Build first (see [Build](#build)), then:

```sh
bin/drop init                    # asks for a reader password
bin/drop open                    # reader, signed in
bin/drop add "Hello app" --slug hello-app
bin/drop list
bin/drop search "hello"
bin/drop sql "SELECT id, name, slug, created FROM apps ORDER BY created DESC LIMIT 20"
bin/drop skill                   # Claude Code, Codex, Antigravity
bin/drop stop
```

Without `--db`, the database is `bin/drop.db`. The server listens on `http://127.0.0.1:8100` (change `port` in `drop.json`, or `init --port N`). Output is JSON; `--pretty` indents it. Run `drop help` for every command and `drop help COMMAND` for one.

`skill` installs `skills/drop` with this computer's absolute binary path and database into `~/.claude/skills/drop`, `~/.codex/skills/drop` and `~/.gemini/antigravity/skills/drop`, skipping agents whose configuration folder is missing (`--agent` selects one, `--dir` a custom folder). With `--url` it installs the remote variant, `SKILL.remote.md`.

## Authentication

A project is single-user: it has exactly one account, created by `init`, with no name or email to choose (`users` holds one hidden record; the server refuses a second one and any deletion, and there is no account management). The reader asks only for its password. `open` signs the browser in with a single-use link valid for two minutes; `login-code` issues one-time codes for remote clients (see below). The CLI needs no password: it reads a random key that the server writes to the private cache at start and exchanges it, in the `X-Drop-Local-Key` header, for an ordinary `users` token. Anyone who can read that key can already read the database. All APIs, including SQL and search, still require a token, CORS allows only the server's own origin, and requests for any other `Host` are refused, so web pages cannot reach the server through `127.0.0.1`. The server listens on loopback only unless [public mode](#public-mode) is explicitly requested. `/api/bundle/instance` answers `{"app": "drop", ...}` so the CLI and the launcher can tell this server from another program on the port.

## Several computers

Only one computer may run a project at a time. `drop.db.lease` carries a heartbeat; another computer refuses to start for two minutes after the last one (`--take-over` overrides when that computer is off). Run `drop stop` before switching computers and wait for the sync client to finish.

If the synced `drop.db` changes while this computer has unsaved changes, nothing is overwritten. This computer's version is kept in `drop.db.conflict-<host>-<time>.db`, the server reports it, and the next start asks for `drop recover --keep local` (the other computer's file is saved as `drop.db.replaced-<time>.db`) or `--keep synced` (this computer's work stays in the conflict copy). Keep the folder available offline: online-only placeholders make the database unavailable.

## Remote client and tunnel

The server can run on one always-on computer (the agent computer) while a person uses the same binary on their own computer as a client only. Sign-in uses one-time codes, so no password passes through whoever operates the agent computer.

On the agent computer, open the tunnel and issue a code. Both computers then use the project's one account:

```sh
drop tunnel serve      # foreground; prints {"address", "port", ...}
drop login-code        # prints {"code","expires"}
```

On the person's computer, forward the same port and sign in with the code on standard input:

```sh
printf '%s\n' "$ADDRESS" | drop tunnel forward --address-stdin   # foreground; or ssh -L 8100:127.0.0.1:8100 <agent>
printf '%s\n' "$CODE" | drop login --url http://127.0.0.1:8100 --code-stdin
drop list --url http://127.0.0.1:8100
drop skill --url http://127.0.0.1:8100
```

For the browser reader, run `drop open --no-browser` on the agent computer and open the printed `signInLink` on the person's computer, or choose "Use a sign-in code instead" in the reader.

The tunnel is built in: [tailcat](https://github.com/tailscale/tailcat) 0.7.0 (WireGuard over Tailscale's data plane, without a Tailscale account, control plane or administrator rights), bootstrapped through tailcat's public relays, which are best-effort and rate-limited, and direct between the computers when the network allows. `tunnel serve` admits only the project's port and proxies it to the server on `127.0.0.1`; there is no other port, no forwarding through the agent computer and no UDP. Its address embeds the server's public key, a pre-shared key and the relay, so anyone with it can reach the port (the server still requires sign-in): share it privately. The identity is kept in the private cache (`tunnel.json`), never in the project folder, so the address stays the same across restarts of `tunnel serve`, and a running `tunnel forward` reconnects on its own; `--new-address` replaces it and invalidates the old one. `tunnel forward` follows the remote-mode rules: it listens only on the loopback port of `--url` (or `DROP_URL`, default `http://127.0.0.1:8100`), fails when that port is busy instead of choosing another, and never reads a database or local key.

Codes and links are single use and kept only in the server's memory, so a server restart invalidates them. A `login-code` code is valid for 10 minutes (`--ttl 30`-`600` seconds); an `open` link for 2 minutes. `login --url URL` without `--code-stdin` signs in with the reader password instead, and `password` on the agent computer changes it (which also ends existing sessions).

A different local port is refused by the server's Host check. `--url` (or `DROP_URL`; an explicit `--db` overrides it) accepts only `http://127.0.0.1:PORT` or `http://localhost:PORT`. Remote mode never resolves or creates a database, never starts a server and never reads the local key. `login` stores only the token, in `os.UserCacheDir()/pocketcontext-drop/remote-<hash>/session.json` (private), and asks for a new sign-in when it expires (7 days) or when the URL serves a different project. `logout` deletes it. Server commands (`serve`, `start`, `ensure-running`, `stop`, `flush`, `recover`, `init`, `password`, `login-code`) run on the agent computer only.

## Public mode

Public mode is opt-in; without `--domain` nothing listens beyond loopback. One persistent service serves the web app on a domain with a Let's Encrypt certificate, and can add a remote MCP endpoint with OAuth:

```sh
drop serve --headless --domain app.example.com [--acme-email EMAIL] [--mcp]
drop open --public     # on the host: a sign-in link for the public origin; never starts a server
```

`--mcp` and `--acme-email` require `--domain`, and `--headless` and `--domain` require each other; `--domain` excludes `--tcp-only`, `--port` and `--ui-dev`, and there is no `--tunnel` option. Public start refuses unless the account password was saved with at least 12 characters, at least 5 of them different; accounts from earlier releases must run `password` first. It needs DNS pointing to the host, reachable ports 80 and 443 and one persistent service (systemd); port 80 serves only ACME validation and redirects, and no reverse proxy is used. `server.json` `"public": {"collections": ["apps"]}` is the allowlist of what the public origin exposes: an unlisted collection is denied, auth or system collections are refused at startup, and so is any entry in `tables` that is not listed, because SQL and search read every entry in `tables`. MCP offers exactly five tools driven by `server.json`: `read`, `search`, `schema`, `query` and `write`, with no shell, filesystem or administration. The project's one account remains the only identity. `--url` accepts only loopback URLs. While a `--domain` service runs there is no loopback TCP port, so data commands, `open` without `--public`, `tunnel serve` and `--url` clients do not reach it; `status`, `stop`, `flush`, `password`, `login-code` and `open --public` do. There is no hosted mode yet. See [HTTPS](docs/https.md) and [MCP](docs/mcp.md). Certificate issuance comes from pocketcontext-core; it has not been exercised with a real certificate for Drop.

## Data model

`apps` (`name`, unique `slug`, `created`, `updated`), the placeholder model, belong to the project: the one account reads and writes them all, and collection rules refuse every other request. Reads use authenticated read-only SQL over the live database (`/api/context/query`) and search (`/api/context/search`); writes use the PocketBase records API. See [docs/data-model.md](docs/data-model.md), including how to add a collection.

## Build

Requires Linux, Go (see `go.mod`) with CGO and a C compiler, Node.js 24 and pnpm 10.33.2. GNU Make runs the targets below (`nix shell nixpkgs#gnumake` where it is missing).

```sh
make ui        # pnpm install --frozen-lockfile and build the reader into ui/dist (embedded by go build)
make build     # bin/drop
make test      # Go unit tests (including the vendored server's), launcher tests, release tests
make names     # retired-name check of the repository and bin/drop (tests/name_check.py)
make identity  # template identity check of the repository and bin/drop (tests/identity_check.py)
make e2e       # names, identity and the anonymous route table, then CLI, remote, tunnel and sync end-to-end tests with synthetic apps
```

`GOOS=darwin go build ./cmd/drop` (or any other system) builds only `cmd/drop/main_other.go`, which exits with "Drop runs on Linux only"; CI checks that no other package is linked into such a build.

### Build tags

`build-tags.txt` is the one list of Go build tags every build, test and release uses. It is copied unchanged from pocketcontext-core: SQLite features (`sqlite_math_functions`, `sqlite_percentile` and `sqlite_fts5`, the functions in the SQL reader's allowlist and the in-memory full-text search index), then the `ts_omit_` tags of the tailcat release in `go.mod`. CI compares it with the copy in the pinned core release. A feature that Drop needs beyond these is added deliberately and extends `internal/appconfig/sqlite_test.go`, which checks that every listed tag is compiled in and that the reader still denies `dbstat`, `pragma_*` functions, `sqlite_schema`, FTS shadow tables and writes. Never add `icu`, `userauth`, `allow_uri_authority`, `secure_delete`, `vacuum_*`, `foreign_keys`, `app_armor` or `os_trace`: they weaken isolation or change storage semantics, and the test refuses them.

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

`node scripts/dev.mjs --db PATH --tunnel` also runs `tunnel serve`, and prints the tunnel address and a one-time sign-in code, so a person on another computer can watch hot reload. They run `tunnel forward --address-stdin --url http://127.0.0.1:PORT` with the **same port** as the project and open `http://127.0.0.1:PORT/bundle/login#CODE`. The same-port rule is not optional: the server accepts only the Host `127.0.0.1:PORT` or `localhost:PORT`, and the development security policy pins the hot-reload WebSocket to `ws://127.0.0.1:PORT`, so a forward on any other port is refused. The address is printed on the terminal only; treat it as a secret.

Hot reload does not type-check. Run `pnpm typecheck`, `pnpm test` and `pnpm build` in `ui/` after changes, plus `node --test tests/dev_launcher.test.mjs` after launcher changes.

The browser checks use synthetic data. Install their pinned Chromium once with `(cd ui && pnpm exec playwright install chromium)`, then run `node tests/reader_ui_browser.mjs --binary bin/drop` (sign-in, adding apps, a duplicate slug, listing and searching at desktop and mobile widths; `--screenshots DIR` keeps screenshots) and `node tests/ui_dev_browser.mjs --binary bin/drop` (hot reload and development routing, including a second browser behind a same-port loopback forward and the refusal of a forward on another port). Both accept `--browser /path/to/chromium`; `ui_dev_browser.mjs` also accepts `--playwright /absolute/path/to/playwright/index.mjs`. CI runs both on Linux amd64 and arm64; they cannot start Chromium on a host without its system libraries.

## Tests

| Command | Checks |
| --- | --- |
| `make test` | Go unit tests with the tags in `build-tags.txt` (application policy, hooks and SQLite features in `internal/appconfig`, client, vendored server; the server and tunnel tests live in pocketcontext-core), `tests/dev_launcher.test.mjs`, `tests/test_release.py` |
| `(cd ui && pnpm typecheck && pnpm test)` | reader types and unit tests (Vitest) |
| `make names` | `tests/name_check.py`: the retired product name appears nowhere in the repository or binary |
| `make identity` | `tests/identity_check.py`: no template identity (name, module, port, hello-world collection, identity fields) left in the repository or binary |
| `make e2e` | `tests/public_routes.py` (on a loopback server: the frozen anonymous route table, foreign-Host refusal and loopback-only listening without `--domain`; refusal of `--hosted`, of invalid public mode options and of `open --public`, and of public start with a weak password; the routes of a running public origin are tested in Go, in `internal/appconfig/public_test.go` and `internal/client/mcp_test.go`) and `tests/e2e.py`: init, CLI, the single account and authentication on every API, SQL policy, search, skill, lease, conflict and recover, refusal of other releases' databases, remote mode, tunnel and login codes, in a temporary folder |
| `node tests/reader_ui_browser.mjs` | the embedded reader in Chromium |
| `node tests/ui_dev_browser.mjs` | hot reload through `serve --ui-dev`, and a same-port loopback forward |

CI (`.github/workflows/test.yml`) runs the reader, unit, release-script, name, identity, end-to-end and browser tests on Linux amd64 (`ubuntu-24.04`) and arm64 (`ubuntu-24.04-arm`), and checks that other systems build only the refusal.

## Releasing

`scripts/release.py` and `.github/workflows/release.yml`: pushing a `vX.Y.Z` tag on `main` builds static musl binaries natively on Linux amd64 and arm64, runs the unit, name, identity and end-to-end checks against each, assembles `drop_<version>_linux_<arch>.tar.gz`, `install.sh` (from `scripts/install.sh.in`) and `checksums.txt`, publishes an immutable GitHub release, then downloads it again with the repository token, checks it against `checksums.txt` and runs the end-to-end test on the published binaries. A manual run of the workflow builds and validates without publishing. Release notes for a version, when present, come from `docs/releases/<version>.md`.

`install.sh` downloads from the release's public URLs, so it works without authentication only once the assets are public:

```sh
curl -fsSL https://github.com/pocketcontext-labs/pocketcontext-drop/releases/latest/download/install.sh | sh -s -- "$HOME/Drop"
```

While the repository is private, download with `gh release download vX.Y.Z -R pocketcontext-labs/pocketcontext-drop` and check `sha256sum -c checksums.txt`. The installer has no default folder: name it as its argument (or `DROP_INSTALL_DIR`). It verifies the archive against `checksums.txt` and installs only the executable.

## Layout

| Path | Content |
| --- | --- |
| `internal/appconfig` | the application's `Identity`, public route policy (deny by default, public collections from `server.json`), maintenance reads, copy and hooks on pocketcontext-core |
| `internal/buildinfo` | version and commit stamped at build time |
| `cmd/drop` | entry point (`main.go`, Linux) and the refusal on every other system (`main_other.go`) |
| `internal/client` | CLI commands, the placeholder apps (`apps.go`), remote mode, skill installation, MCP adapter, help |
| `internal/pocketcontext` | vendored PocketContext server (SQL reads, search, tracing); see `VENDORED.md`. The project, sync-safe database handling, local routes, tunnel and UI development proxy live in [pocketcontext-core](https://github.com/pocketcontext-labs/pocketcontext-core) |
| `pb_hooks`, `pb_migrations` | schema and server-side rules |
| `server.json` | SQL column allowlist, search index configuration and the public collection allowlist |
| `ui` | browser reader (React 19, Vite) |
| `embed.go` | embeds `pb_hooks`, `pb_migrations`, `server.json`, `ui/dist` and `skills/drop` |
| `skills/drop` | skill template (`SKILL.remote.md` for `skill --url`); `drop skill` fills in absolute paths |
| `scripts` | `dev.mjs` launcher, `release.py`, installer template, license notices |
| `tests` | `e2e.py`, `public_routes.py` and `testdata/public_routes.json`, `name_check.py`, `identity_check.py`, `test_release.py`, `dev_launcher.test.mjs`, browser checks |
| `docs` | data model, origin, [HTTPS](docs/https.md) and [MCP](docs/mcp.md) manuals |
