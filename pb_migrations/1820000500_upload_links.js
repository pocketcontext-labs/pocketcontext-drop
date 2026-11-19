// One-time upload links (release.upload_link): a link takes one successful
// PUT /api/drop/upload/<token> of a release within 15 minutes. Only the
// token's SHA-256 is kept (hidden); the token itself is shown once to the
// account that asked for it. Every rule is closed: nobody reads these records
// through the API, and only Drop's Go code writes them.
migrate((app) => {
  const users = app.findCollectionByNameOrId("users");
  const apps = app.findCollectionByNameOrId("apps");
  const releases = app.findCollectionByNameOrId("releases");
  app.save(new Collection({
    name: "upload_links", type: "base",
    listRule: null, viewRule: null, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "app", type: "relation", required: true, collectionId: apps.id, maxSelect: 1, cascadeDelete: false},
      {name: "token_hash", type: "text", required: true, min: 64, max: 64, pattern: "^[0-9a-f]{64}$", hidden: true},
      {name: "created_by", type: "relation", required: true, collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "note", type: "text", max: 500},
      {name: "activate", type: "bool"},
      {name: "expires_at", type: "date", required: true},
      {name: "used_at", type: "date"},
      {name: "release", type: "relation", collectionId: releases.id, maxSelect: 1, cascadeDelete: false},
      {name: "created", type: "autodate", onCreate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_upload_links_token_hash ON upload_links (token_hash)",
      "CREATE INDEX idx_upload_links_expires_at ON upload_links (expires_at)",
    ],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("upload_links"));
});
