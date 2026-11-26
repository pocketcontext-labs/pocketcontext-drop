migrate((app) => {
  // Valid v1 entries are legacy Crew manifests supported by v2. Reinspect
  // old negative entries: new manifests may now qualify for a managed path.
  app.db().newQuery(`INSERT INTO drop_beta_probes(digest,contract,state,manifest)
    SELECT digest,2,state,manifest FROM drop_beta_probes WHERE contract=1 AND state='valid'`).execute();
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new BoolField({name:"generic_beta", hidden:true}));
  app.save(apps);
  const reports = app.findCollectionByNameOrId("node_reports");
  reports.fields.add(new TextField({name:"log_tail", max:1024, hidden:true}));
  app.save(reports);
  app.db().newQuery(`CREATE TABLE drop_manifest_uploads (
    token_hash TEXT PRIMARY KEY, owner TEXT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    digest TEXT NOT NULL, expires_at INTEGER NOT NULL)`).execute();
}, () => { throw new Error("Restore the database backup to roll back."); });
