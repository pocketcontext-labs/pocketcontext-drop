// Pending S3 requests encumber an account's wallet until provider usage settles.
migrate(app => {
  app.db().newQuery(`CREATE TABLE drop_storage_reservations(
    token TEXT PRIMARY KEY, owner TEXT NOT NULL, app TEXT NOT NULL, node TEXT NOT NULL,
    deployment TEXT NOT NULL, class TEXT NOT NULL, byte_limit INTEGER NOT NULL, held INTEGER NOT NULL,
    state TEXT NOT NULL, paid INTEGER NOT NULL DEFAULT 0, markup INTEGER NOT NULL, exchange INTEGER NOT NULL, price INTEGER NOT NULL,
    created INTEGER NOT NULL DEFAULT(unixepoch()))`).execute();
  app.db().newQuery("CREATE INDEX drop_storage_reserved ON drop_storage_reservations(owner,held) WHERE held>0").execute();
  app.db().newQuery("CREATE INDEX drop_storage_pending ON drop_storage_reservations(created) WHERE state='pending'").execute();
}, () => { throw new Error("Restore the pre-upgrade backup to roll back storage reservations."); });
