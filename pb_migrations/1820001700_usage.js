// S3 usage metering (internal/dropserver/usage.go): per app and UTC hour from
// the replica bucket's server access logs, per app and day from its S3
// Inventory. Each account reads the rows of its own apps and admins every
// row; only Drop's Go code writes them. Apps gain admin overrides of their
// monthly soft limits and, for managed Crew apps, the Litestream sync
// interval handed to the guest. Three private SQLite tables hold the
// collector's cursor (processed log objects), its state (the last inventory
// report) and the limit alerts already raised, so a restart never counts or
// alerts twice.
migrate((app) => {
  const visible = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false && (@request.auth.role = 'admin' || app.owner = @request.auth.id)";
  const apps = app.findCollectionByNameOrId("apps");
  const count = (name) => ({name: name, type: "number", min: 0, onlyInt: true});
  app.save(new Collection({
    name: "app_usage_hourly", type: "base",
    listRule: visible, viewRule: visible, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "app", type: "relation", required: true, collectionId: apps.id, maxSelect: 1, cascadeDelete: false},
      {name: "hour", type: "text", required: true, min: 20, max: 20, pattern: "^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:00:00Z$"},
      {name: "source", type: "select", required: true, maxSelect: 1, values: ["s3log"]},
      count("class_a"), count("class_b"), count("deletes"), count("bytes_in"), count("bytes_out"),
      count("status_4xx"), count("status_5xx"), count("denied"),
      {name: "created", type: "autodate", onCreate: true, onUpdate: false},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_app_usage_hourly_key ON app_usage_hourly (app, hour, source)",
      "CREATE INDEX idx_app_usage_hourly_hour ON app_usage_hourly (hour)",
    ],
  }));
  app.save(new Collection({
    name: "app_usage_daily", type: "base",
    listRule: visible, viewRule: visible, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "app", type: "relation", required: true, collectionId: apps.id, maxSelect: 1, cascadeDelete: false},
      {name: "day", type: "text", required: true, min: 10, max: 10, pattern: "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
      count("current_bytes"), count("noncurrent_bytes"), count("objects"), count("versions"),
      {name: "created", type: "autodate", onCreate: true, onUpdate: false},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_app_usage_daily_key ON app_usage_daily (app, day)",
    ],
  }));
  apps.fields.add(new JSONField({ name: "usage_limits", maxSize: 1024 }));
  apps.fields.add(new NumberField({ name: "replica_sync_seconds", min: 0, max: 300, onlyInt: true }));
  app.save(apps);
  app.db().newQuery("CREATE TABLE drop_usage_log_objects (key TEXT PRIMARY KEY NOT NULL, processed_at TEXT NOT NULL, lines INTEGER NOT NULL DEFAULT 0, malformed INTEGER NOT NULL DEFAULT 0)").execute();
  app.db().newQuery("CREATE INDEX idx_drop_usage_log_objects_processed ON drop_usage_log_objects (processed_at)").execute();
  app.db().newQuery("CREATE TABLE drop_usage_state (name TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL)").execute();
  app.db().newQuery("CREATE TABLE drop_usage_alerts (app TEXT NOT NULL, month TEXT NOT NULL, metric TEXT NOT NULL, level TEXT NOT NULL, created TEXT NOT NULL, PRIMARY KEY (app, month, metric, level))").execute();
}, () => {
  throw new Error("Rollback requires restoring the database backup.");
});
