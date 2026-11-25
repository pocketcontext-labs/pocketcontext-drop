package client

// Usage is the help text.
const Usage = `drop — PocketContext Drop in one binary

People use Drop on its hosted origin (serve --hosted: several accounts, email
sign-in, remote MCP). The local project below is for developing the reader.

The project is the database file (default: drop.db beside this binary,
or --db PATH, or DROP_DB), the storage/ folder and drop.json
beside it. They may live in iCloud Drive, OneDrive, Dropbox or Google Drive.

Setup and server:
  init [--port N] [--password-stdin]
                       create the project and its one account (asks for the
                       reader password), start the server
  ensure-running       start the server in the background unless it runs (alias: start)
  serve [--ui-dev URL]  run in the foreground; optionally proxy a loopback Vite UI
  serve --hosted --headless --domain HOST --mcp [--hosted-init | --hosted-restore]
        [--hosted-http-address IP:PORT]
        [--hosted-proxy-header HEADER --hosted-proxy-networks CIDR[,CIDR]] [--fake-aws DIR]
                       hosted mode: plain HTTP behind a TLS-terminating proxy
                       for HOST, email sign-in for the owner (DROP_OWNER_EMAIL,
                       DROP_SMTP_*) and invited people, remote MCP with OAuth
  stop                 write back the database and stop the server
  status               show paths, port, lease and whether the server runs
  open [--no-browser]  open the reader signed in (single-use link, valid for
                       2 minutes); --no-browser prints the link, e.g. to open
                       it on a computer that uses --url
  password [--password-stdin]
                       change the reader password
  login-code [--ttl SECONDS]
                       print a one-time sign-in code (single use, kept in
                       server memory only, default 600 seconds, 30-600) for
                       ` + "`login --url URL --code-stdin`" + `
  flush                write the database back to the synced folder now
  recover --keep local|synced
                       resolve unsaved changes when the synced file also changed
  skill [--agent claude-code|codex|antigravity|all] [--dir DIR]
                       install the agent skill with this computer's paths

Apps (through Drop's operations; APP is an app's id or slug):
  app list [--all] [--archived]      your apps and your app quota
  app show APP                       one app and its secret names
  app create NAME|- --slug SLUG [--kind ordinary|crew] [--mode M] [--port N] [--executable NAME] ...
                                     create an app (counts against your quota)
  app set APP [--name NAME] [--vcpu N] [--memory MIB] [--disk MIB] ...
                                     change settings; a new deployment when
                                     what the app runs with changes
  app env APP KEY=VALUE|-KEY ...     set or remove plain environment variables
  app start|stop|archive|restore APP
  app status APP                     runtime phase: pending, running, sleeping, failed...
  secret list APP                    secret names, versions and lengths
  secret set APP NAME                store a secret value read from standard
                                     input (kept in SSM, never shown again)
  secret delete APP NAME             remove a secret
  release list APP                   uploaded executables, newest first
  release upload APP FILE [--activate] [--note TEXT]
                                     upload a static linux/amd64 executable
                                     (at most 90 MiB) as a new release
  release deploy APP RELEASE_ID      run a release: starts a new deployment

Whole-table reads (the local server or --url; never the hosted origin):
  search TERMS [--limit N] [--offset N --generation G]
                                     rank the apps by full-text match
  sql|query SQL|-                    run one read-only SELECT
  whoami | schema                    the session's account ID; the readable schema

Accounts and nodes (admins; Drop is invite-only, and no invite email is sent):
  invite create EMAIL [--role user|admin] [--quota N] [--expires-days N]
                                     invite an address; prints the sign-in URL to share
  invite list [--state all|open]     invites, newest first, with their state
  invite revoke INVITE_ID            close an open invite
  user list                          accounts with role, quota and suspension
  user set USER_ID|EMAIL [--role user|admin] [--quota N] [--suspend | --unsuspend]
                                     change an account
  node list                          nodes (hosts) with state, online and capacity
  node create NAME --host-id HOST_ID add a node; prints its agent password once
  node rotate-secret NODE            replace a node's password (shown once)
  node set-state NODE active|draining|disabled
  place APP NODE | place APP --clear put an app on a node, or take it off
  usage show APP                     an app's S3 usage, limits and estimated cost
  usage limits APP [--requests-a N] [--requests-b N] [--bytes-out B] [--stored-bytes B] [--reset]
                                     override an app's monthly soft limits
  usage collect                      read new S3 usage logs now (also every 15 minutes)
  op NAME [INPUT_JSON|-] [--idempotency-key KEY]
                                     call any operation (docs/ops.md)
  On a hosted server of this machine these act as the owner through the
  private socket.

Node agent (root on a nibrunner host, drop-agent.service; docs/agent.md):
  agent run            follow this node's desired document and write
                       nibrunner's desired.json; report every 30 seconds
  agent check          run the start checks; writes nothing
  agent render --input FILE|- [--previous FILE]
                       render a drop.desired/v1 document offline (secrets REDACTED)

Remote client (the server runs on another computer, reached through an SSH
forward of the same port; --url URL or DROP_URL):
  login --url URL --code-stdin
                       sign in with a one-time code from ` + "`login-code`" + ` on the
                       agent computer, read from standard input; stores only
                       a token in the private cache
  login --url URL [--password-stdin]
                       sign in with the reader password instead
  logout --url URL     forget the stored session
  status --url URL     show whether the server answers and the sign-in
  open --url URL [--no-browser]
                       open the reader (sign in with the password, or open
                       a link from ` + "`open --no-browser`" + ` run on the agent
                       computer)
  skill --url URL [--agent A] [--dir DIR]
                       install the agent skill for the remote server
  App, secret, release, account, node, place, usage and op commands work with --url. URL must be http://127.0.0.1:PORT or
  http://localhost:PORT. Remote mode never creates a database or starts a
  server; serve, start, ensure-running, stop, flush, recover, init, password
  and login-code are not available with --url.

Run ` + "`drop help COMMAND`" + ` or ` + "`drop COMMAND --help`" + ` for usage, details and an example.
Options for every command: --db PATH or --url URL, --pretty.
--url and --db exclude each other; --db overrides DROP_URL.
Exit codes: 0 ok, 1 HTTP or transport error, 2 usage, 3 agent refused
(operator action needed), 4 HTTP 409.
`
