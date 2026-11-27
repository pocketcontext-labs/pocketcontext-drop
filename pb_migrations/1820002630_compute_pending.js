// Preserve metered seconds while a concurrent S3 exchange encumbers the wallet.
migrate(app => {
  app.db().newQuery("CREATE TABLE drop_credit_resets(owner TEXT PRIMARY KEY, at INTEGER NOT NULL)").execute();
  app.db().newQuery(`CREATE TABLE drop_credit_pending_compute(source TEXT PRIMARY KEY, owner TEXT NOT NULL,
    app TEXT NOT NULL, at INTEGER NOT NULL, seconds INTEGER NOT NULL CHECK(seconds>0), units INTEGER NOT NULL CHECK(units BETWEEN 1 AND 16))`).execute();
  app.db().newQuery("CREATE INDEX drop_credit_pending_owner ON drop_credit_pending_compute(owner,at,source)").execute();
}, () => { throw new Error("Restore the pre-upgrade backup to roll back pending compute accounting."); });
