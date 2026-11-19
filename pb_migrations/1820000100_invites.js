// Invites: Drop is invite-only. An admin invites an address with a role and an
// app quota; Hooks.AllowSignIn admits only an address with an open (unused,
// unrevoked) and unexpired invite, and the account created for it takes the
// invite's role and quota. Only Drop's operations write invites.
migrate((app) => {
  const admin = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false && @request.auth.role = 'admin'";
  const users = app.findCollectionByNameOrId("users");
  app.save(new Collection({
    name: "invites", type: "base",
    listRule: admin, viewRule: admin, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "email", type: "text", required: true, max: 254},
      {name: "email_lower", type: "text", required: true, max: 254},
      {name: "role", type: "select", required: true, values: ["admin", "user"], maxSelect: 1},
      {name: "app_quota", type: "number", min: 0, max: 100, onlyInt: true},
      {name: "invited_by", type: "relation", required: true, collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "expires_at", type: "date", required: true},
      {name: "used_at", type: "date"},
      {name: "used_by", type: "relation", collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "revoked_at", type: "date"},
      {name: "created", type: "autodate", onCreate: true},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: [
      // At most one open invite per address; used and revoked ones stay as history.
      "CREATE UNIQUE INDEX idx_invites_open ON invites (email_lower) WHERE used_at = '' AND revoked_at = ''",
      "CREATE INDEX idx_invites_created ON invites (created)",
      "CREATE INDEX idx_invites_used_by ON invites (used_by)",
    ],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("invites"));
});
