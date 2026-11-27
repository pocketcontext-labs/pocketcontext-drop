// Restrict direct record reads and realtime delivery with the same ownership
// boundary used by Drop operations. Nodes still see their own placements.
migrate((app) => {
  const restrict = (name, condition) => {
    const c = app.findCollectionByNameOrId(name);
    c.listRule = "(" + c.listRule + ") && (" + condition + ")";
    c.viewRule = "(" + c.viewRule + ") && (" + condition + ")";
    app.save(c);
  };
  restrict("apps", "developer_private = false || owner = @request.auth.id");
  const visibleApp = "app.developer_private = false || app.owner = @request.auth.id";
  for (const name of ["releases", "app_secret_names", "app_usage_hourly", "app_usage_daily"]) {
    restrict(name, visibleApp);
  }
  restrict("node_reports", "app = '' || " + visibleApp);
  restrict("placements", "@request.auth.collectionName = 'nodes' || " + visibleApp);

  // Events outlive their app. Persist the private owner even after the
  // PocketBase relation is cleared so deleted app IDs and digests stay private.
  const events = app.findCollectionByNameOrId("events");
  events.fields.add(new TextField({name: "private_owner", max: 15, hidden: true}));
  app.save(events);
  app.db().newQuery("UPDATE events SET private_owner=(SELECT owner FROM apps WHERE id=events.app) WHERE app IN (SELECT id FROM apps WHERE developer_private=1)").execute();
  restrict("events", "private_owner = '' || private_owner = @request.auth.id");

  app.db().newQuery("ALTER TABLE drop_app_deletions ADD COLUMN developer_private INTEGER NOT NULL DEFAULT 0").execute();
  app.db().newQuery("UPDATE drop_app_deletions SET developer_private=1 WHERE app IN (SELECT id FROM apps WHERE developer_private=1)").execute();
}, () => { throw new Error("Restore the database backup to roll back."); });
