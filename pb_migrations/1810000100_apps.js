// The placeholder apps collection of Drop's first scaffold: a name and a slug.
// Drop's real app model (owners, releases, placements) replaces it in later
// milestones; no database with this migration has been released.
migrate((app) => {
  // The project's one account may read and write every record; nobody else can.
  const signedIn = "@request.auth.id != '' && @request.auth.collectionName = 'users'";
  app.save(new Collection({
    name: "apps", type: "base",
    listRule: signedIn, viewRule: signedIn, createRule: signedIn, updateRule: signedIn, deleteRule: signedIn,
    fields: [
      {name: "name", type: "text", required: true, max: 80},
      {name: "slug", type: "text", required: true, min: 3, max: 32, pattern: "^[a-z][a-z0-9-]{1,30}[a-z0-9]$"},
      {name: "created", type: "autodate", onCreate: true},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_apps_slug ON apps (slug)",
      "CREATE INDEX idx_apps_created ON apps (created)",
    ],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("apps"));
});
