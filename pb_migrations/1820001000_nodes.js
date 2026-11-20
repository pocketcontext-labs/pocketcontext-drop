// Nodes: the hosts that run apps, each with one Drop agent (`drop agent`,
// docs/agent-api.md). A node is an auth record of its own collection, never a
// users account: the agent signs in with the node's name and a generated
// password of 40 characters (node.create, node.rotate_secret; at least 32),
// and its token reaches only the agent routes and its own nodes record (for
// the realtime subscription nodes/<id> that wakes it when desired_seq goes
// up). Admins read nodes through the node.list operation; only Drop's Go code
// writes them. A disabled node cannot sign in. Also adds events.actor_node,
// the node behind an event the agent routes write.
migrate((app) => {
  const admin = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.suspended = false && @request.auth.role = 'admin'";
  const self = "@request.auth.id != '' && @request.auth.collectionName = 'nodes' && id = @request.auth.id";
  const nodes = new Collection({
    name: "nodes", type: "auth",
    listRule: admin, viewRule: "(" + admin + ") || (" + self + ")",
    createRule: null, updateRule: null, deleteRule: null, manageRule: null,
    authRule: "state != 'disabled'",
    passwordAuth: {enabled: true, identityFields: ["name"]},
    oauth2: {enabled: false},
    otp: {enabled: false},
    mfa: {enabled: false},
    authAlert: {enabled: false},
    authToken: {duration: 3600},
    fields: [
      {name: "name", type: "text", required: true, min: 2, max: 31, pattern: "^[a-z][a-z0-9-]{1,30}$"},
      // nibrunner's hostId (reported.json); the desired document carries it.
      {name: "host_id", type: "text", required: true, max: 63, pattern: "^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$"},
      {name: "ec2_instance_id", type: "text", max: 32},
      {name: "private_ip", type: "text", max: 45},
      {name: "state", type: "select", required: true, values: ["active", "draining", "disabled"], maxSelect: 1},
      // Goes up whenever the node's desired document changes (seq.go);
      // desired_hash is that document's content hash.
      {name: "desired_seq", type: "number", min: 0, max: 999999999, onlyInt: true},
      {name: "desired_hash", type: "text", max: 64},
      // From the agent's reports (heartbeat.go), written at most every five
      // minutes in steady state.
      {name: "capacity", type: "json", maxSize: 2048},
      {name: "allocatable", type: "json", maxSize: 2048},
      {name: "versions", type: "json", maxSize: 2048},
      {name: "host_state", type: "text", max: 32},
      {name: "agent_version", type: "text", max: 64},
      {name: "accepted_revision", type: "text", max: 128},
      {name: "last_heartbeat", type: "date"},
      {name: "last_report_at", type: "date"},
      {name: "created", type: "autodate", onCreate: true},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_nodes_name ON nodes (name)",
    ],
  });
  app.save(nodes);
  // Nodes have no email: the system field stays, optional and never set.
  const saved = app.findCollectionByNameOrId("nodes");
  saved.fields.getByName("email").required = false;
  saved.fields.getByName("password").min = 32;
  app.save(saved);

  const events = app.findCollectionByNameOrId("events");
  events.fields.add(new RelationField({name: "actor_node", collectionId: saved.id, maxSelect: 1, cascadeDelete: false}));
  app.save(events);
}, (app) => {
  const events = app.findCollectionByNameOrId("events");
  events.fields.removeByName("actor_node");
  app.save(events);
  app.delete(app.findCollectionByNameOrId("nodes"));
});
