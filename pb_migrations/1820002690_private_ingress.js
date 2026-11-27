// Private signing seeds and tester grants are never exposed as collections.
migrate((app) => {
  for (const sql of [
    "CREATE TABLE drop_private_keys (app TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE, seed BLOB NOT NULL CHECK (length(seed)=32))",
    "CREATE TABLE drop_private_testers (app TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE, account TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, PRIMARY KEY(app, account))",
    "CREATE INDEX idx_private_testers_account ON drop_private_testers(account)",
    "INSERT INTO drop_private_keys SELECT id, randomblob(32) FROM apps WHERE developer_private=1",
    "CREATE TRIGGER drop_private_key_create AFTER INSERT ON apps WHEN NEW.developer_private=1 BEGIN INSERT INTO drop_private_keys VALUES (NEW.id,randomblob(32)); END",
    "CREATE TRIGGER drop_private_key_enable AFTER UPDATE OF developer_private ON apps WHEN NEW.developer_private=1 AND OLD.developer_private=0 BEGIN INSERT INTO drop_private_keys VALUES (NEW.id,randomblob(32)); END",
  ]) app.db().newQuery(sql).execute();
}, () => { throw new Error("Restore a backup to roll back private app access."); });
