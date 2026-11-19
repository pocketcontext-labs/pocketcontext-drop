// Idempotency keys of Drop's operations: a retried POST /api/drop/op with the
// same key and input replays the first result instead of acting twice. Rows
// are private to the server (every rule null, never a public route or SQL
// table) and expire after 24 hours. Results an operation shows only once are
// not stored.
migrate((app) => {
  app.save(new Collection({
    name: "op_keys", type: "base",
    listRule: null, viewRule: null, createRule: null, updateRule: null, deleteRule: null,
    fields: [
      {name: "key_hash", type: "text", required: true, min: 64, max: 64, hidden: true},
      {name: "op", type: "text", required: true, max: 64},
      {name: "input_hash", type: "text", required: true, min: 64, max: 64, hidden: true},
      {name: "result", type: "json", maxSize: 1048576, hidden: true},
      {name: "stored", type: "bool"},
      {name: "created", type: "autodate", onCreate: true},
    ],
    indexes: [
      "CREATE UNIQUE INDEX idx_op_keys_key ON op_keys (key_hash)",
      "CREATE INDEX idx_op_keys_created ON op_keys (created)",
    ],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("op_keys"));
});
