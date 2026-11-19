// Apps: one Linux executable each, owned by the account that created it.
// Only Drop's operations write apps (app.*, secret.* and later release.*,
// internal/dropserver); every client write rule is closed. Each account sees
// its own apps and admins see every app. This migration replaced the
// unreleased placeholder 1810000100_apps.js. The releases migration (M5b)
// adds current_release.
migrate((app) => {
  const visible = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false && (@request.auth.role = 'admin' || owner = @request.auth.id)";
  const users = app.findCollectionByNameOrId("users");
  app.save(new Collection({
    name: "apps", type: "base",
    listRule: visible, viewRule: visible, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "owner", type: "relation", required: true, collectionId: users.id, maxSelect: 1, cascadeDelete: false},
      {name: "name", type: "text", required: true, max: 80},
      // Immutable; the hostname and nibrunner ids derive from the slug and id.
      {name: "slug", type: "text", required: true, min: 3, max: 32, pattern: "^[a-z][a-z0-9-]{1,30}[a-z0-9]$"},
      {name: "nib_app_id", type: "text", required: true, min: 17, max: 17, pattern: "^d-[a-z0-9]{15}$"},
      {name: "volume_id", type: "text", required: true, min: 21, max: 21, pattern: "^d-[a-z0-9]{15}-vol$"},
      {name: "hostname", type: "text", max: 253},
      {name: "desired_state", type: "select", required: true, values: ["running", "on-request", "stopped"], maxSelect: 1},
      // The state app.start returns to: the desired state before the app stopped.
      {name: "start_state", type: "select", required: true, values: ["running", "on-request"], maxSelect: 1},
      {name: "idle_timeout_ms", type: "number", required: true, min: 60000, max: 86400000, onlyInt: true},
      {name: "vcpu", type: "number", required: true, min: 1, max: 2, onlyInt: true},
      {name: "memory_mib", type: "number", required: true, min: 128, max: 4096, onlyInt: true},
      {name: "disk_mib", type: "number", required: true, min: 256, max: 16384, onlyInt: true},
      {name: "http_port", type: "number", required: true, min: 1024, max: 65535, onlyInt: true},
      {name: "executable_name", type: "text", required: true, max: 32, pattern: "^[a-z][a-z0-9-]{0,31}$"},
      {name: "args", type: "json", maxSize: 4096},
      // Plain (non-secret) environment; secrets live in SSM (app_secret_names).
      {name: "env", type: "json", maxSize: 32768},
      {name: "health_kind", type: "select", required: true, values: ["tcp", "http"], maxSelect: 1},
      {name: "health_path", type: "text", max: 1024},
      // The deployment number: <nib_app_id>-dep-<n>, bumped whenever the
      // instance configuration changes (nibrunner ignores a changed
      // configuration under the same deployment id).
      {name: "deployment_seq", type: "number", required: true, min: 1, max: 999999999, onlyInt: true},
      {name: "archived", type: "bool"},
      {name: "archived_at", type: "date"},
      {name: "created", type: "autodate", onCreate: true},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_apps_slug ON apps (slug)",
      "CREATE UNIQUE INDEX idx_apps_nib_app_id ON apps (nib_app_id)",
      "CREATE INDEX idx_apps_owner_created ON apps (owner, created)",
    ],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("apps"));
});
