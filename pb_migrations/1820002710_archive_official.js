migrate(app => {
  app.db().newQuery("ALTER TABLE drop_official_releases ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1))").execute();
}, () => { throw new Error("Restore the database backup to roll back."); });
