// Private tables keep mailbox challenges and delivery state off collection APIs.
migrate((app) => {
  for (const sql of [
    "CREATE TABLE drop_waitlist_key (secret BLOB NOT NULL)",
    "INSERT INTO drop_waitlist_key VALUES(randomblob(32))",
    `CREATE TABLE drop_waitlist (id TEXT PRIMARY KEY, email TEXT UNIQUE NOT NULL,
      terms TEXT NOT NULL, requested INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
      verified_at TEXT NOT NULL DEFAULT '', activated_at TEXT NOT NULL DEFAULT '', invite_id TEXT NOT NULL DEFAULT '')`,
    `CREATE TABLE drop_waitlist_mail (id TEXT PRIMARY KEY, entry TEXT NOT NULL,
      kind TEXT NOT NULL, generation INTEGER NOT NULL, due INTEGER NOT NULL,
      attempts INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'queued')`,
    `CREATE TRIGGER drop_waitlist_activated AFTER UPDATE OF verified,beta_terms_pending ON users
      WHEN NEW.verified=1 AND NEW.beta_terms_pending=0 BEGIN
        UPDATE drop_waitlist SET activated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
        WHERE activated_at='' AND invite_id IN (SELECT id FROM invites WHERE used_by=NEW.id);
      END`,
    "CREATE INDEX drop_waitlist_mail_due ON drop_waitlist_mail(state,due)",
  ]) app.db().newQuery(sql).execute();
}, () => { throw new Error("Restore the pre-upgrade backup."); });
