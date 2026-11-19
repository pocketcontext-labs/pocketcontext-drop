// Releases: the executables uploaded for each app, one row per app and
// content. The bytes live in the release store (S3, or the --fake-aws
// directory) at releases/<sha256>, written by Drop's upload route with
// if-absent semantics; there is no PocketBase file field (its storage layout
// is its own). Also adds apps.current_release, the release an app runs, set by
// the release.deploy operation. Only Drop's Go code writes either.
migrate((app) => {
  const visible = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false && (@request.auth.role = 'admin' || app.owner = @request.auth.id)";
  const users = app.findCollectionByNameOrId("users");
  const apps = app.findCollectionByNameOrId("apps");
  const releases = new Collection({
    name: "releases", type: "base",
    listRule: visible, viewRule: visible, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "app", type: "relation", required: true, collectionId: apps.id, maxSelect: 1, cascadeDelete: false},
      {name: "sha256", type: "text", required: true, min: 64, max: 64, pattern: "^[0-9a-f]{64}$"},
      {name: "size_bytes", type: "number", required: true, min: 1, max: 94371840, onlyInt: true},
      {name: "s3_key", type: "text", required: true, min: 73, max: 73, pattern: "^releases/[0-9a-f]{64}$"},
      {name: "note", type: "text", max: 500},
      {name: "created_by", type: "relation", collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "created", type: "autodate", onCreate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_releases_app_sha256 ON releases (app, sha256)",
      "CREATE INDEX idx_releases_app_created ON releases (app, created)",
    ],
  });
  app.save(releases);
  apps.fields.add(new RelationField({name: "current_release", collectionId: releases.id, maxSelect: 1, cascadeDelete: false}));
  app.save(apps);
}, (app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.removeByName("current_release");
  app.save(apps);
  app.delete(app.findCollectionByNameOrId("releases"));
});
