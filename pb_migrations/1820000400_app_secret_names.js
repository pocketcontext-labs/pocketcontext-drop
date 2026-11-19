// The names of each app's secrets. Values never touch the database: the
// secret.set operation stores them in SSM (<prefix>/apps/<app id>/<NAME>) and
// records here only the name, the SSM version, the value's length and how it
// was set. Only Drop's operations write these rows.
migrate((app) => {
  const visible = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false && (@request.auth.role = 'admin' || app.owner = @request.auth.id)";
  const users = app.findCollectionByNameOrId("users");
  const apps = app.findCollectionByNameOrId("apps");
  app.save(new Collection({
    name: "app_secret_names", type: "base",
    listRule: visible, viewRule: visible, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "app", type: "relation", required: true, collectionId: apps.id, maxSelect: 1, cascadeDelete: false},
      {name: "name", type: "text", required: true, max: 64, pattern: "^[A-Z][A-Z0-9_]{0,63}$"},
      {name: "ssm_version", type: "number", required: true, min: 1, onlyInt: true},
      {name: "length", type: "number", required: true, min: 1, max: 4096, onlyInt: true},
      {name: "set_via", type: "select", required: true, values: ["ui", "mcp"], maxSelect: 1},
      {name: "updated_by", type: "relation", collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "created", type: "autodate", onCreate: true},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_app_secret_names_app_name ON app_secret_names (app, name)",
    ],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("app_secret_names"));
});
