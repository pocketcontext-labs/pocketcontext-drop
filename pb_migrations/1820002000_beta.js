// Catalog approvals and probe attempts are server-private. Neither table is a
// PocketBase collection or a SQL-reader table. Existing apps remain operator-managed.
migrate((app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new TextField({ name: "beta_digest", max: 64, hidden: true }));
  app.save(apps);
  app.db().newQuery("CREATE TABLE drop_beta_catalog (digest TEXT PRIMARY KEY, approved INTEGER NOT NULL)").execute();
  app.db().newQuery(`CREATE TABLE drop_beta_probes (
    digest TEXT NOT NULL REFERENCES drop_beta_catalog(digest),
    contract INTEGER NOT NULL, state TEXT NOT NULL, attempt TEXT NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL DEFAULT 0, manifest TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (digest, contract))`).execute();
}, () => {
  throw new Error("Rollback requires restoring the database backup.");
});
