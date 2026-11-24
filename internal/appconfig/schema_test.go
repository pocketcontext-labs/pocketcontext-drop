package appconfig

import (
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-core/appserver"

	assets "github.com/pocketcontext-labs/pocketcontext-drop"
)

func TestAppHTTPPortMigration(t *testing.T) {
	app := newHookApp(t)
	const portMigration = "1820001400_app_http_port.js"
	var previous, upgrade core.MigrationsList
	for _, migration := range core.AppMigrations.Items() {
		if migration.File < portMigration {
			previous.Add(migration)
		} else if migration.File == portMigration {
			upgrade.Add(migration)
		}
	}
	if _, err := core.NewMigrationsRunner(app, previous).Up(); err != nil {
		t.Fatal(err)
	}

	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	owner := core.NewRecord(users)
	owner.SetEmail("owner@example.test")
	owner.SetRandomPassword()
	owner.Set("role", "admin")
	if err := app.Save(owner); err != nil {
		t.Fatal(err)
	}
	apps, err := app.FindCollectionByNameOrId("apps")
	if err != nil {
		t.Fatal(err)
	}
	record := core.NewRecord(apps)
	record.Load(map[string]any{
		"owner": owner.Id, "name": "Existing app", "slug": "existing-app",
		"nib_app_id": "d-aaaaaaaaaaaaaaa", "volume_id": "d-aaaaaaaaaaaaaaa-vol",
		"desired_state": "stopped", "start_state": "running", "idle_timeout_ms": 300000,
		"vcpu": 1, "memory_mib": 512, "disk_mib": 1024, "http_port": 8080,
		"executable_name": "server", "health_kind": "tcp", "deployment_seq": 2,
	})
	if err := app.Save(record); err != nil {
		t.Fatal(err)
	}
	record, err = app.FindRecordById("apps", record.Id)
	if err != nil {
		t.Fatal(err)
	}
	before := record.FieldsData()
	record.Set("http_port", 80)
	if err := app.Validate(record); err == nil {
		t.Fatal("pre-upgrade schema accepted port 80")
	}

	if _, err := core.NewMigrationsRunner(app, upgrade).Up(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := app.FindRecordById("apps", record.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, upgraded.FieldsData()) {
		t.Fatal("migration changed the existing app")
	}
	upgraded.Set("http_port", 80)
	if err := app.Save(upgraded); err != nil {
		t.Fatalf("saving port 80 after upgrade: %v", err)
	}
}

func migrationsDB(t *testing.T, files ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if files == nil {
		if _, err := db.Exec("CREATE TABLE other(id)"); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := db.Exec("CREATE TABLE _migrations(file TEXT PRIMARY KEY, applied INTEGER)"); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, err := db.Exec("INSERT INTO _migrations VALUES (?, 0)", f); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// TestCheckDatabaseRefusesOtherReleases: a database whose schema came from
// migrations this release does not ship, such as one of another product or an
// earlier schema, is refused before PocketBase
// opens it; a new or current database is accepted.
func TestCheckDatabaseRefusesOtherReleases(t *testing.T) {
	entries, err := fs.ReadDir(assets.Assets, "pb_migrations")
	if err != nil || len(entries) == 0 {
		t.Fatal("no embedded migrations", err)
	}
	var current []string
	for _, m := range core.SystemMigrations.Items() {
		current = append(current, m.File) // PocketBase's own migrations are Go
	}
	for _, e := range entries {
		current = append(current, e.Name())
	}
	for name, path := range map[string]string{
		"empty":   migrationsDB(t),
		"current": migrationsDB(t, current...),
	} {
		if err := Runtime.CheckDatabase(path, "project.db"); err != nil {
			t.Errorf("%s database refused: %v", name, err)
		}
	}
	for name, files := range map[string][]string{
		"other schema": {"1800000000_users.js"},
		"newer":        append(append([]string{}, current...), "1890000000_more.js"),
		"foreign go":   {"1000000000_other.go"},
	} {
		err := Runtime.CheckDatabase(migrationsDB(t, files...), "project.db")
		if !errors.Is(err, appserver.ErrEarlierRelease) {
			t.Errorf("%s database accepted: %v", name, err)
			continue
		}
		if !strings.Contains(err.Error(), "different "+Identity.DisplayName+" release") {
			t.Errorf("%s: unclear refusal: %v", name, err)
		}
	}
}
