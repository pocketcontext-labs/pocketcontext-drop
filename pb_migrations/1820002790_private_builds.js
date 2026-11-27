migrate((app) => {
  app.db().newQuery(`CREATE TABLE drop_private_build_links (
    owner TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE, account_key TEXT NOT NULL, expires_at INTEGER NOT NULL
  )`).execute();
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new BoolField({name: "private_tiered", hidden: true}));
  app.save(apps);
}, () => { throw new Error("Restore the database backup to roll back."); });
