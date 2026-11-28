migrate((app) => {
  for (const statement of [
    `CREATE TABLE drop_backup_policies (app TEXT PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE, next_at INTEGER NOT NULL)`,
    `CREATE TABLE drop_backups (
      id TEXT PRIMARY KEY, owner TEXT NOT NULL, app TEXT NOT NULL, source_app TEXT NOT NULL, name TEXT NOT NULL,
      release_sha TEXT NOT NULL, multiplier INTEGER NOT NULL, state TEXT NOT NULL, created INTEGER NOT NULL,
      sha256 TEXT NOT NULL DEFAULT '', bytes INTEGER NOT NULL DEFAULT 0)`,
    `CREATE INDEX drop_backups_owner ON drop_backups(owner,created DESC)`,
    `CREATE TABLE drop_backup_jobs (id TEXT PRIMARY KEY, backup_id TEXT NOT NULL REFERENCES drop_backups(id) ON DELETE CASCADE,
      kind TEXT NOT NULL, target TEXT NOT NULL, node TEXT NOT NULL, instance_id TEXT NOT NULL, state TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL)`,
    `CREATE UNIQUE INDEX drop_backup_jobs_active ON drop_backup_jobs(target) WHERE state='pending'`,
    `CREATE INDEX drop_backup_jobs_node ON drop_backup_jobs(node,state)`
  ]) app.db().newQuery(statement).execute();
}, () => { throw new Error("Restore the control-plane database backup to roll back."); });
