package appconfig

import (
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

func TestExclusiveRolesMigration(t *testing.T) {
	app := newHookApp(t)
	const filename = "1820002750_exclusive_roles.js"
	var old, upgrade core.MigrationsList
	for _, m := range core.AppMigrations.Items() {
		if m.File < filename {
			old.Add(m)
		} else if m.File == filename {
			upgrade.Add(m)
		}
	}
	if _, err := core.NewMigrationsRunner(app, old).Up(); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := app.DB().NewQuery(q).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email,role,developer,tokenKey) VALUES
		('admin','admin@example.test','admin',1,'a'),
		('dev','developer@example.test','user',1,'b'),
		('user','user@example.test','user',0,'c')`)
	exec(`INSERT INTO apps(id,slug,owner,developer_private) VALUES ('app','preview','admin',1)`)
	if _, err := core.NewMigrationsRunner(app, upgrade).Up(); err == nil || !strings.Contains(err.Error(), "private app") {
		t.Fatalf("expected admin ownership to block migration, got %v", err)
	}
	// Model the operator resolving this fixture's ownership, then retry the same migration.
	exec(`UPDATE apps SET owner='dev' WHERE id='app'`)
	exec(`INSERT INTO drop_private_testers(app,account) VALUES ('app','admin'),('app','user')`)
	var oldSeed, newSeed string
	readSeed := func(seed *string) {
		t.Helper()
		if err := app.DB().NewQuery("SELECT hex(seed) FROM drop_private_keys WHERE app='app'").Row(seed); err != nil {
			t.Fatal(err)
		}
	}
	readSeed(&oldSeed)
	if _, err := core.NewMigrationsRunner(app, upgrade).Up(); err != nil {
		t.Fatal(err)
	}
	readSeed(&newSeed)
	var tester string
	if err := app.DB().NewQuery("SELECT group_concat(account) FROM drop_private_testers WHERE app='app'").Row(&tester); err != nil || tester != "user" || oldSeed == newSeed {
		t.Fatal("admin tester migration", tester, err)
	}
	for id, want := range map[string]string{"admin": "admin", "dev": "developer", "user": "user"} {
		r, err := app.FindRecordById("users", id)
		if err != nil {
			t.Fatal(err)
		}
		if r.GetString("role") != want || r.Collection().Fields.GetByName("developer") != nil {
			t.Errorf("unexpected post-migration role fields for %s", id)
		}
	}
	r, err := app.FindRecordById("apps", "app")
	if err != nil || r.GetString("owner") != "dev" || !r.GetBool("developer_private") {
		t.Fatal("private workspace ownership changed", err)
	}
}

func TestOwnerOnlyPrivateAppsMigration(t *testing.T) {
	app := newHookApp(t)
	const filename = "1820002800_owner_only_private_apps.js"
	var old, upgrade core.MigrationsList
	for _, m := range core.AppMigrations.Items() {
		if m.File < filename {
			old.Add(m)
		} else if m.File == filename {
			upgrade.Add(m)
		}
	}
	if _, err := core.NewMigrationsRunner(app, old).Up(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO users(id,email,role,tokenKey) VALUES ('dev','dev@example.test','developer','d'),('user','user@example.test','user','u')",
		"INSERT INTO apps(id,slug,owner,developer_private,nib_app_id,volume_id) VALUES ('solo','solo','dev',1,'d-solo','v-solo'),('shared','shared','dev',1,'d-shared','v-shared')",
		"INSERT INTO drop_private_testers(app,account) VALUES ('shared','user')",
		"CREATE TABLE old_keys AS SELECT * FROM drop_private_keys",
	} {
		if _, err := app.DB().NewQuery(q).Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := core.NewMigrationsRunner(app, upgrade).Up(); err != nil {
		t.Fatal(err)
	}
	var rotated string
	if err := app.DB().NewQuery("SELECT k.app FROM drop_private_keys k JOIN old_keys o ON o.app=k.app WHERE k.seed != o.seed").Row(&rotated); err != nil || rotated != "shared" {
		t.Fatalf("expected to fence tickets for the formerly shared app; affected %q, err %v", rotated, err)
	}
	var tables int
	if err := app.DB().NewQuery("SELECT count(*) FROM sqlite_schema WHERE name='drop_private_testers'").Row(&tables); err != nil || tables != 0 {
		t.Fatalf("tester table remains after migration: %d, err %v", tables, err)
	}
}
