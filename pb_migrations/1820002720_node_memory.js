// One complete, concurrent guest memory measurement per host sampling pass.
migrate(app => {
  app.db().newQuery(`CREATE TABLE drop_node_memory (
    node TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    at INTEGER NOT NULL, used_mib INTEGER NOT NULL, running_vms INTEGER NOT NULL,
    PRIMARY KEY(node, at)
  ) WITHOUT ROWID`).execute();
  app.db().newQuery("CREATE INDEX drop_node_memory_at ON drop_node_memory(at)").execute();
}, app => {
  app.db().newQuery("DROP TABLE drop_node_memory").execute();
});
