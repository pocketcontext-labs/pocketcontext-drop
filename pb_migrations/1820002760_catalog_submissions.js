migrate(app => {
  app.db().newQuery(`CREATE TABLE drop_catalog_submissions (
    release TEXT PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,
    submitted_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product TEXT NOT NULL CHECK(product IN ('crew','atlas')),
    version TEXT NOT NULL, digest TEXT NOT NULL,
    disclosure_approved_by TEXT NOT NULL DEFAULT '',
    submitted_at INTEGER NOT NULL DEFAULT (unixepoch()),
    accepted INTEGER NOT NULL DEFAULT 0 CHECK(accepted IN (0,1))
  )`).execute();
}, () => { throw new Error("Restore the database backup to roll back catalog submissions."); });
