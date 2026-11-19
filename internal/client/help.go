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
		"drop help add"},
	"version": {"version", "Print the version and commit of this binary.", "drop version"},

	"init": {"init [--port N] [--password-stdin]",
		"Create the project (database, storage/ and drop.json) and its one account, asking for the reader password, then start the server. The account has no name or email to choose. An existing account keeps its password unless --password-stdin is given.",
		"printf '%s\\n' \"$PASSWORD\" | drop init --password-stdin"},
	"ensure-running": {"ensure-running [--take-over]",
		"Start the server in the background unless it already runs for this database (alias: start). --take-over ignores a lease from another computer; use it only when that computer is off.",
		"drop ensure-running"},
	"start": {"start [--take-over]", "Alias of ensure-running.", "drop start"},
	"serve": {"serve [--take-over] [--ui-dev URL] | serve --hosted --headless --domain HOST --mcp [--hosted-init | --hosted-restore] [--hosted-http-address IP:PORT] [--hosted-proxy-header HEADER --hosted-proxy-networks CIDR[,CIDR]] [--fake-aws DIR] [--take-over]", "Run the server in the foreground until interrupted. Plain serve is the local single-account server on loopback, for developing the reader: --ui-dev http://127.0.0.1:5173 serves the reader through a loopback Vite development server (APIs and sign-in remain on this server; foreground only, never saved in project configuration). --hosted is how people use Drop: several accounts, email sign-in and remote MCP with OAuth on HOST, served as plain HTTP behind a TLS-terminating proxy (docs/hosting.md). It reads DROP_OWNER_EMAIL and DROP_SMTP_HOST, _PORT, _USERNAME, _PASSWORD, _FROM and _TLS from the environment. --hosted-init creates a new hosted database and refuses an existing one; ordinary hosted startup refuses a missing database. --hosted-restore invalidates copied browser, email and MCP credentials before serving a restored backup. --hosted-http-address sets the listen address (a literal IP and port, default :80). --hosted-proxy-header (X-Forwarded-For or CF-Connecting-IP) with --hosted-proxy-networks trusts that header from those proxy networks for per-client sign-in limits. Hosted mode keeps releases in S3 and secrets in SSM through the default AWS credential chain (DROP_AWS_REGION, DROP_S3_BUCKET, DROP_SSM_PREFIX; docs/aws.md); --fake-aws DIR keeps them in a local directory instead, for testing, and only with a loopback listen address. Local administration stays private.", "drop serve --hosted --headless --mcp --domain drop.example.com --hosted-http-address 127.0.0.1:8100 --hosted-proxy-header CF-Connecting-IP --hosted-proxy-networks 127.0.0.0/8"},
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
		"drop sql \"SELECT id, name, slug, created FROM apps ORDER BY created DESC LIMIT 20\""},
	"query": {"query SQL|-", "Alias of sql.", "drop query \"SELECT count(*) FROM apps\""},
	"add": {"add NAME|- --slug SLUG",
		"Add an app with a name (1-80 characters) and a unique slug (3-32 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen). `-` reads the name from standard input. A slug already in use is refused. Prints {\"id\", \"name\", \"slug\", \"created\", \"updated\"}.",
		"drop add \"Hello app\" --slug hello-app"},
	"list": {"list [--limit N]",
		"List the apps, newest first (default 50, at most 500), as an array of {\"id\", \"name\", \"slug\", \"created\", \"updated\"}.",
		"drop list --limit 10"},
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
