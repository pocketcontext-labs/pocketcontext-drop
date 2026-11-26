package dropserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/awsx"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// Secret operations (M5a, decision 9). A secret is an environment variable
// whose value lives only in SSM, at awsx.SecretParameter(prefix, nib_app_id,
// NAME). The server holds a write-only store (Services.Secrets): it puts and
// deletes values and never reads one. The database keeps the name, the SSM
// version, the value's length and set_via; no operation, event, error or log
// line ever carries a value. secret.set ignores idempotency keys
// (Spec.SecretInput), so not even a hash of a value is stored.
//
// SSM puts run inside the writer transaction and a rolled-back version goes
// unreferenced. SSM deletes run after the database commits the removal and
// its cleanup entry; outstanding deletions survive process restarts.

func init() {
	register("secret.list", secretList)
	register("secret.set", secretSet)
	register("secret.delete", secretDelete)
}

// secretStoreTimeout bounds one SSM call (the database transaction waits on it).
const secretStoreTimeout = 15 * time.Second

func secretRecords(app core.App, appRecord *core.Record) ([]*core.Record, error) {
	return app.FindRecordsByFilter("app_secret_names", "app = {:app}", "name", 0, 0, dbx.Params{"app": appRecord.Id})
}

func secretOut(r *core.Record) dropapi.Secret {
	return dropapi.Secret{
		Name: r.GetString("name"), Version: r.GetInt("ssm_version"), Length: r.GetInt("length"),
		SetVia: r.GetString("set_via"), UpdatedBy: r.GetString("updated_by"), Updated: dateString(r, "updated"),
	}
}

// secretsOf lists an app's secrets by name.
func secretsOf(app core.App, appRecord *core.Record) ([]dropapi.Secret, error) {
	records, err := secretRecords(app, appRecord)
	if err != nil {
		return nil, err
	}
	out := []dropapi.Secret{}
	for _, r := range records {
		out = append(out, secretOut(r))
	}
	return out, nil
}

// secretNames lists the names of an app's secrets.
func secretNames(app core.App, appRecord *core.Record) ([]string, error) {
	records, err := secretRecords(app, appRecord)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(records))
	for _, r := range records {
		names = append(names, r.GetString("name"))
	}
	return names, nil
}

func findSecret(app core.App, appRecord *core.Record, name string) (*core.Record, error) {
	var records []*core.Record
	err := app.RecordQuery("app_secret_names").AndWhere(dbx.HashExp{"app": appRecord.Id, "name": name}).Limit(1).All(&records)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, sql.ErrNoRows
	}
	return records[0], nil
}

// Store errors may include submitted values in arbitrary encodings.
func (c *Ctx) secretStoreFailed(action string) error {
	c.App.Logger().Error("Drop secret store failed", "action", action)
	return &dropapi.Error{Status: http.StatusBadGateway, Code: dropapi.CodeSecretStoreFailed,
		Message: "The secret store did not answer. Please retry."}
}

func (c *Ctx) storeContext() (context.Context, context.CancelFunc) {
	parent := context.Background()
	if c.Request != nil {
		parent = c.Request.Context()
	}
	return context.WithTimeout(parent, secretStoreTimeout)
}

func secretList(c *Ctx, in *dropapi.SecretListInput) (any, error) {
	app, err := c.findApp(in.App, false)
	if err != nil {
		return nil, err
	}
	secrets, err := secretsOf(c.App, app)
	if err != nil {
		return nil, err
	}
	return dropapi.SecretList{Secrets: secrets}, nil
}

func secretSet(c *Ctx, in *dropapi.SecretSetInput) (any, error) {
	app, err := c.writableApp(in.App)
	if err != nil {
		return nil, err
	}
	if app.GetString("beta_digest") != "" {
		return nil, Forbidden("Drop manages the environment for this Beta deployment.")
	}
	if reason := appspec.CheckAppEnvName(in.Name, app.GetString("crew_enrollment") != ""); reason != "" {
		return nil, Invalid("name", "name: "+reason+".")
	}
	if in.Value == "" || len(in.Value) > dropapi.MaxSecretBytes {
		return nil, Invalid("value", fmt.Sprintf("A secret value is 1-%d bytes.", dropapi.MaxSecretBytes))
	}
	if reason := appspec.CheckEnvValue(in.Name, in.Value); reason != "" {
		return nil, Invalid("value", reason+".")
	}
	if _, plain := appEnv(app)[in.Name]; plain {
		return nil, Invalid("name", in.Name+" is a plain environment variable of this app; unset it first.")
	}
	existing, err := findSecret(c.App, app, in.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if existing == nil {
		names, err := secretNames(c.App, app)
		if err != nil {
			return nil, err
		}
		if len(appEnv(app))+len(names)+1 > appspec.MaxEnvEntries {
			return nil, Invalid("name", fmt.Sprintf("An app has at most %d environment variables and secrets together.", appspec.MaxEnvEntries))
		}
	}
	services := servicesOf(c.App)
	if services.Secrets == nil {
		return nil, secretsUnavailable()
	}
	parameter, err := awsx.SecretParameter(services.SSMPrefix, app.GetString("nib_app_id"), in.Name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := c.storeContext()
	defer cancel()
	// Serialize recreation with a previous deletion. Only the transaction that
	// removes the durable cleanup entry may write a new version at this path.
	if err := clearSecretDeletion(ctx, c.App, parameter); err != nil {
		return nil, c.secretStoreFailed("cleanup")
	}
	version, err := services.Secrets.Put(ctx, parameter, in.Value)
	if err != nil {
		return nil, c.secretStoreFailed("put")
	}
	record := existing
	if record == nil {
		collection, err := c.App.FindCachedCollectionByNameOrId("app_secret_names")
		if err != nil {
			return nil, err
		}
		record = core.NewRecord(collection)
		record.Set("app", app.Id)
		record.Set("name", in.Name)
	}
	record.Set("ssm_version", version)
	record.Set("length", len(in.Value))
	record.Set("set_via", c.via())
	record.Set("updated_by", c.Auth.Id)
	if err := c.App.Save(record); err != nil {
		return nil, err
	}
	if err := bumpDeployment(app); err != nil {
		return nil, err
	}
	if err := c.App.Save(app); err != nil {
		return nil, err
	}
	c.Event(Event{App: app.Id, Detail: map[string]any{"name": in.Name, "version": version, "set_via": c.via(), "deployment_seq": app.GetInt("deployment_seq")}})
	return dropapi.SecretSetResult{Secret: secretOut(record), DeploymentBumped: true, DeploymentID: deploymentID(app)}, nil
}

func secretDelete(c *Ctx, in *dropapi.SecretDeleteInput) (any, error) {
	app, err := c.writableApp(in.App)
	if err != nil {
		return nil, err
	}
	record, err := findSecret(c.App, app, in.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFound("Secret")
	}
	if err != nil {
		return nil, err
	}
	services := servicesOf(c.App)
	if services.Secrets == nil {
		return nil, secretsUnavailable()
	}
	parameter, err := awsx.SecretParameter(services.SSMPrefix, app.GetString("nib_app_id"), in.Name)
	if err != nil {
		return nil, err
	}
	if err := c.App.Delete(record); err != nil {
		return nil, err
	}
	if err := bumpDeployment(app); err != nil {
		return nil, err
	}
	if err := c.App.Save(app); err != nil {
		return nil, err
	}
	// The event, desired state, removed name and deletion intent commit together.
	// A failed commit leaves the old parameter and its reference intact.
	_, err = c.App.DB().Insert("drop_secret_deletions", dbx.Params{"parameter": parameter}).Execute()
	if err != nil {
		return nil, err
	}
	c.deleteParameter = parameter
	c.Event(Event{App: app.Id, Detail: map[string]any{"name": in.Name, "deployment_seq": app.GetInt("deployment_seq")}})
	return dropapi.SecretDeleteResult{Deleted: in.Name, DeploymentBumped: true, DeploymentID: deploymentID(app)}, nil
}
