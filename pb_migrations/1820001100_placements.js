// Placements: which node runs an app (one node per app in v0), set and
// cleared by admins with placement.set and placement.clear. Admins read them;
// a node reads its own; only Drop's Go code writes them. Also adds
// apps.first_placed_at: an app's disk is fixed from its first placement on
// (the volume exists on the node), even after the placement is cleared.
migrate((app) => {
  const admin = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false && @request.auth.role = 'admin'";
  const self = "@request.auth.id != '' && @request.auth.collectionName = 'nodes' && node = @request.auth.id";
  const users = app.findCollectionByNameOrId("users");
  const apps = app.findCollectionByNameOrId("apps");
  const nodes = app.findCollectionByNameOrId("nodes");
  app.save(new Collection({
    name: "placements", type: "base",
    listRule: "(" + admin + ") || (" + self + ")", viewRule: "(" + admin + ") || (" + self + ")",
    createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "app", type: "relation", required: true, collectionId: apps.id, maxSelect: 1, cascadeDelete: false},
      {name: "node", type: "relation", required: true, collectionId: nodes.id, maxSelect: 1, cascadeDelete: false},
      // Goes up each time the app moves to another node.
      {name: "generation", type: "number", required: true, min: 1, onlyInt: true},
      {name: "created_by", type: "relation", collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "created", type: "autodate", onCreate: true},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_placements_app ON placements (app)",
      "CREATE INDEX idx_placements_node ON placements (node)",
    ],
  }));
  apps.fields.add(new DateField({name: "first_placed_at"}));
  app.save(apps);
}, (app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.removeByName("first_placed_at");
  app.save(apps);
  app.delete(app.findCollectionByNameOrId("placements"));
});
