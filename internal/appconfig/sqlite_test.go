package appconfig

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/pocketcontext/server"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/pocketcontext/sqlread"
)

// The SQLite tags of build-tags.txt, which is copied unchanged from
// pocketcontext-core, and the compile option each one enables.
var sqliteTagOptions = map[string]string{
	"sqlite_math_functions": "ENABLE_MATH_FUNCTIONS",
	"sqlite_percentile":     "ENABLE_PERCENTILE",
	"sqlite_fts5":           "ENABLE_FTS5",
}

// Tags that must stay out: they weaken isolation or change storage semantics.
var forbiddenSQLiteTags = []string{"sqlite_icu", "icu", "sqlite_userauth", "sqlite_allow_uri_authority", "sqlite_secure_delete",
	"sqlite_secure_delete_fast", "sqlite_vacuum_full", "sqlite_vacuum_incr", "sqlite_foreign_keys", "sqlite_app_armor", "sqlite_os_trace"}

func buildTags(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "build-tags.txt"))
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]bool{}
	for _, tag := range strings.Split(strings.TrimSpace(string(data)), ",") {
		tags[tag] = true
	}
	return tags
}

func TestSQLiteCompileOptions(t *testing.T) {
	tags := buildTags(t)
	for tag := range sqliteTagOptions {
		if !tags[tag] {
			t.Errorf("build-tags.txt lacks %s", tag)
		}
	}
	for tag := range tags {
		if strings.HasPrefix(tag, "sqlite_") {
			if _, known := sqliteTagOptions[tag]; !known {
				t.Errorf("build-tags.txt enables %s, which this test does not check", tag)
			}
		}
	}
	for _, tag := range forbiddenSQLiteTags {
		if tags[tag] {
			t.Errorf("build-tags.txt must not enable %s", tag)
		}
	}

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.Query("SELECT compile_options FROM pragma_compile_options")
	if err != nil {
		t.Fatal(err)
	}
	options := map[string]bool{}
	for rows.Next() {
		var option string
		if err := rows.Scan(&option); err != nil {
			t.Fatal(err)
		}
		option, _, _ = strings.Cut(option, "=")
		options[option] = true
	}
	rows.Close()
	for tag, option := range sqliteTagOptions {
		if option != "" && !options[option] {
			t.Errorf("%s: compile option %s missing (have %v)", tag, option, options)
		}
	}
	for _, option := range []string{"USER_AUTHENTICATION", "SECURE_DELETE", "ENABLE_ICU", "DEFAULT_FOREIGN_KEYS", "ENABLE_API_ARMOR"} {
		if options[option] {
			t.Errorf("unexpected compile option %s", option)
		}
	}

	// Each feature also works, not only its flag.
	for _, statement := range []string{
		"SELECT round(sqrt(16) + pi(), 2)",
		"SELECT median(value) FROM json_each('[1,2,9]')",
		"CREATE VIRTUAL TABLE probe_fts USING fts5(body)",
		"CREATE TABLE probe(id INTEGER PRIMARY KEY, v)",
		"INSERT INTO probe(v) VALUES (1), (2)",
		"SELECT count(*) FROM pragma_function_list",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Errorf("%s: %v", statement, err)
		}
	}
}

// TestSQLReaderDeniesSQLiteInternals runs the shipped server.json policy over a
// database with an FTS5 table and checks that the extra SQLite features stay
// out of reach of the authenticated SQL reader: no dbstat, no pragma_*
// table-valued functions, no schema table, no FTS shadow tables, no writes.
func TestSQLReaderDeniesSQLiteInternals(t *testing.T) {
	cfg, err := server.LoadConfig(filepath.Join("..", "..", "server.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;
		CREATE TABLE users(id TEXT PRIMARY KEY, email TEXT, password TEXT, tokenKey TEXT);
		CREATE TABLE apps(id TEXT PRIMARY KEY, owner TEXT, name TEXT, slug TEXT, nib_app_id TEXT, hostname TEXT, desired_state TEXT,
			idle_timeout_ms INTEGER, vcpu INTEGER, memory_mib INTEGER, disk_mib INTEGER, http_port INTEGER, executable_name TEXT,
			health_kind TEXT, health_path TEXT, deployment_seq INTEGER, current_release TEXT, archived BOOLEAN, archived_at TEXT, env JSON, created TEXT, updated TEXT);
		CREATE TABLE app_secret_names(id TEXT PRIMARY KEY, app TEXT, name TEXT, ssm_version INTEGER, length INTEGER, set_via TEXT,
			updated_by TEXT, created TEXT, updated TEXT);
		CREATE TABLE releases(id TEXT PRIMARY KEY, app TEXT, sha256 TEXT, size_bytes INTEGER, s3_key TEXT, note TEXT, created_by TEXT,
			created TEXT);
		CREATE TABLE events(id TEXT PRIMARY KEY, actor TEXT, actor_node TEXT, app TEXT, subject_user TEXT, action TEXT, detail JSON, created TEXT);
		CREATE TABLE placements(id TEXT PRIMARY KEY, app TEXT, node TEXT, generation INTEGER, created_by TEXT, created TEXT, updated TEXT);
		CREATE TABLE node_reports(id TEXT PRIMARY KEY, node TEXT, app TEXT, nib_app_id TEXT, deployment_id TEXT, phase TEXT, message TEXT,
			restart_count INTEGER, last_exit_code INTEGER, has_exit_code BOOLEAN, converged_at TEXT, observed_at TEXT);
		CREATE VIRTUAL TABLE apps_fts USING fts5(name);
		INSERT INTO users VALUES ('account00000001','account@example.localhost','hash-a','key-a');
		INSERT INTO apps(id, owner, name, slug, env, created, updated) VALUES ('record000000001','account00000001','Hello','hello','{}','2026-01-01','2026-01-01'),
			('record000000002','account00000001','Hello again','hello-again','{}','2026-01-02','2026-01-02');
		INSERT INTO apps_fts(name) SELECT name FROM apps;`); err != nil {
		t.Fatal(err)
	}
	queryCfg := sqlread.Config{Tables: cfg.Tables, Timeout: time.Duration(cfg.TimeoutMS) * time.Millisecond, MaxRows: cfg.MaxRows, MaxBytes: cfg.MaxBytes}
	// The reader queries the live database file, which holds every table, so
	// its authorizer alone must refuse the statements below.
	engine, err := sqlread.New(path, queryCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ctx := context.Background()

	r, err := engine.Query(ctx, "SELECT name FROM apps ORDER BY created")
	if err != nil || len(r.Rows) != 2 || r.Rows[0][0] != "Hello" {
		t.Fatalf("allowlisted read: %#v %v", r.Rows, err)
	}
	if r, err := engine.Query(ctx, "SELECT median(length(name)), sqrt(4) FROM apps"); err != nil || len(r.Rows) != 1 {
		t.Fatalf("math and percentile functions in the reader: %#v %v", r.Rows, err)
	}

	denied := []string{
		"SELECT * FROM dbstat",
		"SELECT * FROM dbstat('main')",
		"SELECT name, pgsize FROM dbstat WHERE name = 'apps'",
		"SELECT * FROM pragma_table_info('apps')",
		"SELECT * FROM pragma_compile_options",
		"SELECT * FROM pragma_database_list",
		"SELECT * FROM pragma_function_list",
		"SELECT * FROM pragma_index_list('apps')",
		"PRAGMA table_info('apps')",
		"PRAGMA compile_options",
		"SELECT * FROM sqlite_schema",
		"SELECT * FROM sqlite_master",
		"SELECT sql FROM sqlite_temp_master",
		"SELECT * FROM sqlite_stat1",
		"SELECT * FROM apps_fts",
		"SELECT * FROM apps_fts WHERE apps_fts MATCH 'hello'",
		"SELECT * FROM apps_fts_data",
		"SELECT * FROM apps_fts_idx",
		"SELECT * FROM apps_fts_content",
		"SELECT * FROM apps_fts_docsize",
		"SELECT * FROM apps_fts_config",
		"SELECT password FROM users",
		"SELECT * FROM users",
		"SELECT email FROM users",
		"SELECT env FROM apps",
		"INSERT INTO apps(id, name) VALUES ('x', 'y')",
		"UPDATE apps SET name = 'changed'",
		"DELETE FROM apps",
		"CREATE TABLE extra(id)",
		"DROP TABLE apps",
		"ATTACH DATABASE ':memory:' AS other",
		"PRAGMA writable_schema = 1",
		"VACUUM INTO 'copy.db'",
		"SELECT load_extension('x')",
		"SELECT 1; DELETE FROM apps",
	}
	for _, query := range denied {
		if r, err := engine.Query(ctx, query); err == nil {
			t.Errorf("reader allowed %q: %#v", query, r.Rows)
		}
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM apps").Scan(&n); err != nil || n != 2 {
		t.Fatalf("the database changed: %d %v", n, err)
	}
}
