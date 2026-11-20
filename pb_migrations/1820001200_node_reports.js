// Node reports: the last phase each node reported for each app placed on it
// (POST /api/drop/agent/report, reports.go), one row per node and app,
// rewritten only when the phase, message or deployment changes. Each account
// reads the rows of its own apps and admins every row; only Drop's Go code
// writes them.
migrate((app) => {
  const visible = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false && (@request.auth.role = 'admin' || app.owner = @request.auth.id)";
  const apps = app.findCollectionByNameOrId("apps");
  const nodes = app.findCollectionByNameOrId("nodes");
  app.save(new Collection({
    name: "node_reports", type: "base",
    listRule: visible, viewRule: visible, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "node", type: "relation", required: true, collectionId: nodes.id, maxSelect: 1, cascadeDelete: false},
      {name: "app", type: "relation", collectionId: apps.id, maxSelect: 1, cascadeDelete: false},
      {name: "nib_app_id", type: "text", required: true, min: 17, max: 17, pattern: "^d-[a-z0-9]{15}$"},
      {name: "deployment_id", type: "text", max: 40},
      {name: "phase", type: "select", required: true, maxSelect: 1, values: [
        "pending", "staging", "staging_failed", "secret_failed", "deploying", "starting",
        "running", "sleeping", "unhealthy", "stopped", "failed", "unknown",
      ]},
      {name: "message", type: "text", max: 2000},
      {name: "restart_count", type: "number", min: 0, onlyInt: true},
      {name: "last_exit_code", type: "number", onlyInt: true},
      {name: "has_exit_code", type: "bool"},
      {name: "converged_at", type: "text", max: 40},
      // When this phase was first reported (the row is not rewritten while
      // nothing changes).
      {name: "observed_at", type: "date"},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_node_reports_node_app ON node_reports (node, nib_app_id)",
      "CREATE INDEX idx_node_reports_app ON node_reports (app)",
    ],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("node_reports"));
});
