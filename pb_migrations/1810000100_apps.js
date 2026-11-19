// The placeholder apps collection: a name and a slug, owned by the account
// that created it. Drop's real app model (releases, secrets, placements, writes
// only through Drop's operations) replaces it in later milestones; no database
// with this migration has been released.
migrate((app) => {
  // Each account sees and changes only its own apps. internal/dropserver sets
  // the owner of every create request to the requester and never moves it.
  const signedIn = "@request.auth.id != '' && @request.auth.collectionName = 'users'";
  const own = signedIn + " && owner = @request.auth.id";
  const users = app.findCollectionByNameOrId("users");
  app.save(new Collection({
    name: "apps", type: "base",
    listRule: own, viewRule: own, createRule: signedIn, updateRule: own, deleteRule: own,
    fields: [
      {name: "owner", type: "relation", required: true, collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "name", type: "text", required: true, max: 80},
      {name: "slug", type: "text", required: true, min: 3, max: 32, pattern: "^[a-z][a-z0-9-]{1,30}[a-z0-9]$"},
      {name: "created", type: "autodate", onCreate: true},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_apps_slug ON apps (slug)",
      "CREATE INDEX idx_apps_owner_created ON apps (owner, created)",
    ],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("apps"));
});
