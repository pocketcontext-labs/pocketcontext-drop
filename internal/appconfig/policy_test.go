package appconfig

import (
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/plugins/jsvm"
)

// TestRealHooksAllowAccountsButKeepSignedIn: account_access.js accepts more
// than one account (hosted Multi) and lets core prune accounts that never
// signed in, but refuses to delete one that has, whatever deletes it.
func TestRealHooksAllowAccountsButKeepSignedIn(t *testing.T) {
	app := newHookApp(t)
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	account := func(email string, verified bool) *core.Record {
		record := core.NewRecord(users)
		record.SetEmail(email)
		record.SetRandomPassword()
		record.SetVerified(verified)
		record.Set("role", "user") // the Go hooks (not loaded here) set it on real saves
		if err := app.Save(record); err != nil {
			t.Fatalf("%s: %v", email, err)
		}
		return record
	}
	owner := account("owner@example.test", true)
	pending := account("pending@example.test", false)
	if err := app.Delete(owner); err == nil || !strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("deleting a signed-in account: %v", err)
	}
	if err := app.Delete(pending); err != nil {
		t.Fatalf("pruning an account that never signed in: %v", err)
	}
	if count, err := app.CountRecords("users"); err != nil || count != 1 {
		t.Fatalf("accounts = %d, %v", count, err)
	}
}

func newHookApp(t *testing.T) core.App {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	app := core.NewBaseApp(core.BaseAppConfig{DataDir: t.TempDir()})
	jsvm.MustRegister(app, Runtime.HooksConfig(root, ""))
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app
}
