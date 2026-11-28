package client

import (
	"fmt"
	"strings"
)

// helpEntry is the text of `help COMMAND` and `COMMAND --help`.
type helpEntry struct{ usage, text, example string }

// commandHelp has one entry for every command in the dispatch table of run
// and for the commands handled before it (help, version).
var commandHelp = map[string]helpEntry{
	"help": {"help [COMMAND]",
		"Print the command overview, or the usage, description and an example of one command. `COMMAND --help` does the same.",
		"drop help app"},
	"version": {"version", "Print the version and commit of this binary.", "drop version"},

	"init": {"init [--port N] [--password-stdin]",
		"Create the project (database, storage/ and drop.json) and its one account, asking for the reader password, then start the server. The account has no name or email to choose. An existing account keeps its password unless --password-stdin is given.",
		"printf '%s\\n' \"$PASSWORD\" | drop init --password-stdin"},
	"ensure-running": {"ensure-running [--take-over]",
		"Start the server in the background unless it already runs for this database (alias: start). --take-over ignores a lease from another computer; use it only when that computer is off.",
		"drop ensure-running"},
	"start": {"start [--take-over]", "Alias of ensure-running.", "drop start"},
	"serve": {"serve [--take-over] [--ui-dev URL] | serve --hosted --headless --domain HOST --mcp [--hosted-init | --hosted-restore] [--hosted-http-address IP:PORT] [--hosted-proxy-header HEADER --hosted-proxy-networks CIDR[,CIDR]] [--fake-aws DIR] [--take-over]", "Run the server in the foreground until interrupted. Plain serve is the local single-account server on loopback, for developing the reader: --ui-dev http://127.0.0.1:5173 serves the reader through a loopback Vite development server (APIs and sign-in remain on this server; foreground only, never saved in project configuration). --hosted is how people use Drop: several accounts, email sign-in and remote MCP with OAuth on HOST, served as plain HTTP behind a TLS-terminating proxy (docs/hosting.md). It reads DROP_OWNER_EMAIL, DROP_SMTP_HOST, _PORT, _USERNAME, _PASSWORD, _FROM and _TLS, and optionally DROP_APP_DOMAIN (app hostnames are SLUG.DROP_APP_DOMAIN) from the environment. --hosted-init creates a new hosted database and refuses an existing one; ordinary hosted startup refuses a missing database. --hosted-restore invalidates copied browser, email and MCP credentials before serving a restored backup. --hosted-http-address sets the listen address (a literal IP and port, default :80). --hosted-proxy-header (X-Forwarded-For or CF-Connecting-IP) with --hosted-proxy-networks trusts that header from those proxy networks for per-client sign-in limits. Hosted mode keeps releases in S3 and secrets in SSM through the default AWS credential chain (DROP_AWS_REGION, DROP_S3_BUCKET, DROP_SSM_PREFIX; docs/aws.md); --fake-aws DIR keeps them in a local directory instead, for testing, and only with a loopback listen address. Local administration stays private.", "drop serve --hosted --headless --mcp --domain drop.example.com --hosted-http-address 127.0.0.1:8100 --hosted-proxy-header CF-Connecting-IP --hosted-proxy-networks 127.0.0.0/8"},
	"stop":  {"stop", "Write the database back to the synced folder and stop the server. Run it before switching computers.", "drop stop"},
	"status": {"status",
		"Show paths, port, lease, write-back conflicts and whether the server runs. With --url: whether the server answers and whether you are signed in.",
		"drop status"},
	"open": {"open [--no-browser]",
		"Open the local reader signed in through a single-use link valid for 2 minutes. With --no-browser, print the link, e.g. to open it on a computer that uses --url.",
		"drop open --no-browser"},
	"flush": {"flush", "Write the database back to the synced folder now.", "drop flush"},
	"recover": {"recover --keep local|synced",
		"Resolve unsaved local changes when the synced database also changed elsewhere: keep this computer's copy or the synced one. The other copy is kept beside it.",
		"drop recover --keep synced"},
	"password": {"password [--password-stdin]", "Change the reader password of the project's account.", "drop password"},
	"login-code": {"login-code [--ttl SECONDS]",
		"Print a one-time sign-in code (single use, kept in server memory only, default 600 seconds, 30-600) for `login --url URL --code-stdin`.",
		"drop login-code --ttl 120"},
	"skill": {"skill [--agent claude-code|codex|antigravity|all] [--dir DIR]",
		"Install the agent skill with this computer's binary path and database (or, with --url, the remote server). Agents whose configuration folder is missing are skipped.",
		"drop skill --agent claude-code"},
	"login": {"login --url URL (--code-stdin | [--password-stdin])",
		"Sign in to a forwarded server, with a one-time code from `login-code` read from standard input, or with the reader password. Only a token is stored, in the private cache.",
		"printf '%s\\n' \"$CODE\" | drop login --url http://127.0.0.1:8100 --code-stdin"},
	"logout": {"logout --url URL", "Forget the stored session for that server.", "drop logout --url http://127.0.0.1:8100"},

	"whoami": {"whoami", "Show the ID of the project's account and the server URL, which proves the session works.", "drop whoami"},
	"schema": {"schema", "Print the live SQL-readable schema: tables, columns and types, and the search indexes.", "drop schema"},
	"sql": {"sql SQL|-",
		"Run one read-only SELECT over the readable tables (alias: query). Results are bounded: when `truncated` is true, select fewer columns or page with ORDER BY and LIMIT/OFFSET. `-` reads the query from standard input.",
		"drop sql \"SELECT id, name, slug, desired_state, created FROM apps ORDER BY created DESC LIMIT 20\""},
	"query": {"query SQL|-", "Alias of sql.", "drop query \"SELECT count(*) FROM apps\""},
	"app": {"app list [--all] [--archived] [--limit N] | app show APP | app create NAME|- --slug SLUG [--kind ordinary|crew] [settings] | app set APP [--name NAME] [--sign-in-email enabled|disabled] [settings] | app env APP KEY=VALUE|-KEY ... | app start|stop|archive|restore APP | app status APP",
		"Your apps, through Drop's operations; APP is an app's id or slug. `app list` prints {\"apps\", \"has_more\", \"quota\"} newest first (--archived: archived apps; --all: every account's, admins only). `app show` prints {\"app\", \"secrets\"} (secret names only). `app create` makes an app with a name (1-80 characters) and a unique slug (3-32 lowercase letters, digits and hyphens; its hostname is SLUG.<app domain>) and prints {\"app\"}; its volume counts against your 10 GiB retained-disk allowance (exit 4 with quota_exceeded when full). Settings: --mode running|on-request|stopped (default on-request), --idle-timeout MS (60000-86400000, default 300000), --vcpu 1-2, --memory 128-4096 MiB (default 512), --disk 256-16384 MiB (default 1024), --port 1-65535 (guest HTTP port, default 8080), --hook-port 0-65535 (0 disables private lifecycle hooks), --executable NAME (default server, crew for Crew apps), --args-json '[\"serve\"]', --health tcp|http, --health-path /PATH. `app set` changes settings (also --replica-sync-interval 1s-300s, a Crew app's Litestream sync interval, default 10s) and prints {\"app\", \"deployment_bumped\"}: a change to what the app runs with starts a new deployment. `app env` sets KEY=VALUE and removes -KEY plain environment variables (never secrets: use `secret set`). `app start` returns a stopped app to running or on-request; `app archive` stops it and retains its allocated disk; `app restore` brings it back. `app status` prints the app's runtime status: {\"phase\", \"message\", \"deployment_id\", \"reported_deployment_id\", \"placed\", \"restart_count\", \"last_exit_code\", ...}, the phase being archived, no_release, unplaced, pending, deploying, staging, starting, running, sleeping, unhealthy, stopped, failed or unknown (admins also see the node).",
		"drop app create \"Hello app\" --slug hello-app --port 8080"},
	"secret": {"secret list APP | secret set APP NAME | secret delete APP NAME",
		"An app's secrets: environment variables whose values live only in AWS SSM, never in the database, a log or an answer. `secret set` reads the value (1-4096 bytes, one trailing newline removed) from standard input, never from an argument, stores it as a new version and starts a new deployment; it prints {\"secret\", \"deployment_bumped\", \"deployment_id\"}. `secret list` prints {\"secrets\"}: name, version, length, set_via and updated, never values. `secret delete` removes the name and schedules deletion of every stored version. Names are A-Z, 0-9 and _ (not HOME, PATH, NIBRUN_* or DROP_*). The server needs a secret store: a hosted Drop, or --fake-aws (exit 1 with secrets_unavailable otherwise).",
		"printf '%s' \"$TOKEN\" | drop secret set hello-app API_TOKEN"},
	"release": {"release list APP [--limit N] | release upload APP FILE [--activate] [--note TEXT] | release deploy APP RELEASE_ID",
		"An app's releases: the Linux executables it runs. `release upload` checks that FILE is a static linux/amd64 ELF executable of at most 90 MiB (for Go: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build), asks for a one-time upload link and PUTs the file to it; it prints {\"release\", \"created\", \"deployed\", \"deployment_id\"} (created is false when the app already had a release of the same bytes). --activate also deploys it; --note keeps a short note (at most 500 characters). `release list` prints {\"releases\", \"has_more\", \"current_release\"} newest first, each with id, sha256, size_bytes, s3_key, note, created and current. `release deploy` makes a release the app's current one and starts a new deployment; it prints {\"app\", \"release\", \"deployment_bumped\", \"deployment_id\"}. The server needs a release store: a hosted Drop, or --fake-aws (exit 1 with releases_unavailable otherwise). Without the CLI, the operation release.upload_link returns a URL for `curl -fT FILE URL`.",
		"drop release upload hello-app ./server --activate"},
	"invite": {"invite create EMAIL [--role user|developer|admin] [--quota N] [--expires-days N] | invite list [--state all|open] [--limit N] | invite revoke INVITE_ID",
		"Admins only. Drop is invite-only: `invite create` invites an address (role user by default, valid 7 days, at most 30) and prints {\"invite\", \"sign_in_url\"}; Drop emails admin invitations. Share customer invitation links directly. The address's first sign-in creates its account with the invite's role and quota. `invite list` prints {\"invites\", \"has_more\"} newest first, each with its state (open, used, revoked or expired). `invite revoke` closes an open invite. An address that already has an account or an open invite is refused (exit 4). On a hosted server of this machine the command acts as the owner through the private socket.",
		"drop invite create friend@example.com --role user"},
	"user": {"user list [--limit N] | user set USER_ID|EMAIL [--role user|developer] [--quota N] [--suspend | --unsuspend]",
		"Admins only. `user list` prints {\"users\", \"has_more\"} newest first, each with id, email, role, app_quota, suspended, owner and signed_in. `user set` changes an account's role, app quota (0-100) or suspension; suspending ends its sessions at once. Nobody changes their own role or suspension, and the owner is always an unsuspended admin.",
		"drop user set friend@example.com --suspend"},
	"node": {"node list [--limit N] | node create NAME --host-id HOST_ID [--ec2-instance-id ID] [--private-ip IP] | node rotate-secret NODE | node set-state NODE active|draining|disabled",
		"Admins only. Nodes are the hosts that run apps, each with one Drop agent that signs in as its node. `node create` (alias add) adds a node with a name (2-31 lowercase letters, digits and hyphens; the agent's sign-in identity) and nibrunner's host id (hostId in reported.json) and prints {\"node\", \"password\", \"identity\", \"collection\"}: the 40-character password is shown once, for the agent's node-secret credential (docs/agent-api.md). `node rotate-secret` prints a new password the same way and ends the old one. `node set-state` makes a node active, draining (keeps its apps, takes no new ones) or disabled (its agent is locked out). `node list` prints {\"nodes\", \"has_more\"} by name, with state, online, desired_seq, nibrunner's capacity, allocatable and versions, and the number of apps placed.",
		"drop node create walter-aws --host-id host-0123"},
	"place": {"place APP NODE | place APP --clear",
		"Admins only. Place any account's app on an active node (by id or name), or move it to another one, and print {\"placement\", \"moved\", \"previous_node\"}; the node runs it once it has a deployed release. A moved app stops on the old node, which keeps its copy of the data (volumes do not move in v0). The app's disk size is fixed from its first placement. --clear removes the placement (the node stops the app and keeps its data) and prints {\"app\", \"cleared_node\"}. The node must be able to allocate the app's vCPUs and memory (exit 4 with node_too_small).",
		"drop place hello-app walter-aws"},
	"usage": {"usage show APP | usage limits APP [--requests-a N] [--requests-b N] [--bytes-out BYTES] [--stored-bytes BYTES] [--reset] | usage collect",
		"An app's S3 usage, metered from the replica bucket's server access logs and daily inventory (docs/hosting.md). `usage show` (your own apps; admins any) prints {\"app\", \"month\", \"hourly\", \"month_to_date\", \"stored\", \"limits\", \"estimate\", \"exceeded\", \"metered\"}: the last 24 hours by hour, the month to date against the soft and hard limits (Class A and DELETE requests 100,000, Class B 1,000,000, bytes sent 2 GB, stored 1 GB per month; hard limits are ten times these) and an estimated cost (an estimate, not a bill). Past a soft limit Drop records one app.usage_exceeded event; past a hard limit it also stops the app once. `usage limits` (admins) overrides an app's soft limits and prints {\"app\", \"override\", \"limits\"}; --reset returns to the defaults. `usage collect` (admins) runs the collector once (it also runs every 15 minutes) and prints what it read.",
		"drop usage show my-crew"},
	"private-ingress": {"private-ingress --listen IP:PORT --upstream IP:PORT --app-domain DOMAIN --control-origin https://DROP", "Run the loopback browser gateway ahead of the NibRunner HTTP hostname proxy. Use separate loopback addresses for the listener and upstream. See docs/private-ingress.md.", "drop private-ingress --listen 127.0.0.1:8081 --upstream 127.0.0.1:8080 --app-domain apps.example.com --control-origin https://drop.example.com"},
	"agent": {"agent run|check [--server URL] [--host DOMAIN] [--node NAME] [--secret-file FILE] [--desired FILE] [--reported FILE] [--artifacts DIR] [--state-dir DIR] [--allowed-host-suffix .DOMAIN] [--ssm-prefix /PREFIX] [--fake-aws DIR] | agent render --input FILE|- [--previous FILE] [--allowed-host-suffix .DOMAIN] [--ssm-prefix /PREFIX] | agent host-services [--desired FILE] [--state-dir DIR] [--socket FILE] | agent manifest-probe",
		"The node agent, run as root on a nibrunner host by drop-agent.service (docs/agent.md). Options override the DROP_AGENT_* variables of /etc/drop/agent.env; the node secret is read from a file (systemd LoadCredential=node-secret, or --secret-file), never from an argument or variable. `agent run` signs in as its node, follows its desired document (realtime wake-ups plus a full pull every 60 seconds), stages releases by sha256, reads secret versions from SSM, writes nibrunner's whole desired.json atomically (the previous file kept as desired.json.drop-prev) and reports every 30 seconds; it never writes desired.json when the server is unreachable or answers an error. `agent check` runs the start checks (desired.json, reported.json, sign-in, the host id, each release in S3 and each secret version in SSM), writes nothing and prints {\"ok\", \"refused\", \"node\", \"hostId\", \"checks\"}. `agent render` renders a drop.desired/v1 document offline against --previous (default DROP_AGENT_DESIRED) with secret values REDACTED and prints {\"revision\", \"contentHash\", \"changed\", \"rejected\", \"kept\", \"tombstoned\", \"document\"}. Drop owns the whole desired.json: when it holds any entry whose appId does not start with d-, run and check refuse with exit 3 and name the entries, and nothing is written; the operator removes them. --fake-aws DIR reads releases and secrets from a local directory, for testing, and only with a loopback server URL. Exit codes: 0 ok, 1 failure, 2 usage or configuration, 3 refused (operator action needed).",
		"drop agent check --pretty"},
	"op": {"op NAME [INPUT_JSON|-] [--idempotency-key KEY]",
		"Call one of Drop's operations (POST /api/drop/op) with a JSON object as input (`-` reads it from standard input) and print its result. A retry with the same --idempotency-key and input within 24 hours replays the first result instead of acting twice. Errors print the HTTP status and a stable code; HTTP 409 is exit 4. See docs/ops.md for every operation.",
		"drop op invite.list '{\"state\":\"open\"}'"},
	"search": {"search TERMS [--limit N] [--offset N --generation G]",
		"Rank the apps by full-text match of their name and slug. 1-16 terms, each matched as a whole word (all must match). Prints {\"query\", \"generation\", \"apps\", \"hasMore\", \"nextOffset\"}. To page, pass the returned generation with --offset; a generation conflict (exit 4) means the apps changed: restart at offset 0.",
		"drop search \"hello\" --limit 10"},
}

// printHelp prints the help of one command, or the overview for "".
func printHelp(command string) error {
	if command == "" {
		fmt.Print(Usage)
		return nil
	}
	h, ok := commandHelp[command]
	if !ok {
		return fail(2, "unknown command %q; run `drop help`", command)
	}
	fmt.Printf("usage: drop %s\n\n%s\n\nExample:\n  %s\n", h.usage, wrap(h.text, 78), h.example)
	if command != "help" && command != "version" {
		fmt.Println("\nOptions for every command: --db PATH or --url URL, --pretty.")
	}
	return nil
}

// wrap folds each one-line paragraph at width; indented blocks stay as written.
func wrap(text string, width int) string {
	paragraphs := strings.Split(text, "\n\n")
	for i, p := range paragraphs {
		if strings.HasPrefix(p, " ") || strings.Contains(p, "\n") {
			continue
		}
		var lines []string
		line := ""
		for _, word := range strings.Fields(p) {
			if line != "" && len(line)+1+len(word) > width {
				lines, line = append(lines, line), ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		paragraphs[i] = strings.Join(append(lines, line), "\n")
	}
	return strings.Join(paragraphs, "\n\n")
}

// wantsHelp reports whether the arguments ask for help (before any "--").
func wantsHelp(argv []string) bool {
	for _, arg := range argv {
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

// helpTopic returns the command named after `help`, skipping global options.
func helpTopic(rest []string) (string, error) {
	a, err := parseArgs(rest, nil, nil)
	if err != nil {
		return "", err
	}
	if len(a.pos) > 1 {
		return "", fail(2, "usage: drop help [COMMAND]")
	}
	if len(a.pos) == 0 {
		return "", nil
	}
	return strings.TrimSpace(a.pos[0]), nil
}
