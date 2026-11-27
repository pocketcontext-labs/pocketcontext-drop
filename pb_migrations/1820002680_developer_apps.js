// Private developer apps never publish a legacy, unprotected hostname.
migrate((app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new BoolField({name: "developer_private"}));
  apps.fields.add(new TextField({name: "private_release_master", hidden: true, max: 64}));
  app.save(apps);
  // A previously public address may still be routed by an older node. Never
  // reuse a private hostname either, even after deletion or a binary downgrade.
  app.db().newQuery("CREATE TABLE drop_private_slugs (slug TEXT PRIMARY KEY)").execute();
  app.db().newQuery(`CREATE TRIGGER drop_private_slug_guard BEFORE INSERT ON apps
    WHEN EXISTS(SELECT 1 FROM drop_private_slugs WHERE slug=NEW.slug)
      OR (NEW.developer_private=1 AND EXISTS(SELECT 1 FROM drop_app_deletions WHERE slug=NEW.slug))
    BEGIN SELECT RAISE(ABORT,'Retired hostname'); END`).execute();
  app.db().newQuery(`CREATE TRIGGER drop_private_slug_reserve AFTER INSERT ON apps WHEN NEW.developer_private=1
    BEGIN INSERT INTO drop_private_slugs VALUES (NEW.slug); END`).execute();
  const releases = app.findCollectionByNameOrId("releases");
  releases.fields.add(new JSONField({name: "private_manifest", hidden: true, maxSize: 16384}));
  app.save(releases);
}, () => { throw new Error("Restore a backup to roll back private developer apps."); });
