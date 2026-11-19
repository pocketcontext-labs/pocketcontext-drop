// Events: one row per change made through Drop's operations (and, later, the
// agent routes): who, which app or account, the operation and a small detail
// object that never holds secret values. Only Drop's Go code writes them.
migrate((app) => {
  const users = app.findCollectionByNameOrId("users");
  const apps = app.findCollectionByNameOrId("apps");
  const visible = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false && (@request.auth.role = 'admin' || (app != '' && app.owner = @request.auth.id))";
  app.save(new Collection({
    name: "events", type: "base",
    listRule: visible, viewRule: visible, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "actor", type: "relation", collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "app", type: "relation", collectionId: apps.id, maxSelect: 1, cascadeDelete: false},
      {name: "subject_user", type: "relation", collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "action", type: "text", required: true, max: 64, pattern: "^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)*$"},
      {name: "detail", type: "json", maxSize: 8192},
      {name: "created", type: "autodate", onCreate: true},
    ],
    indexes: [
      "CREATE INDEX idx_events_app_created ON events (app, created)",
      "CREATE INDEX idx_events_created ON events (created)",
    ],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("events"));
});
