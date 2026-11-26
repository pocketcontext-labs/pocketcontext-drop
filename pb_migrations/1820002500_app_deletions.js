// Private durable work queues. Slugs can be reused only after the app's
// writers have been fenced and external cleanup has finished.
migrate((app) => {
  app.db().newQuery(`CREATE TABLE drop_app_deletions (
    app TEXT PRIMARY KEY, owner TEXT NOT NULL, slug TEXT NOT NULL,
    nib TEXT NOT NULL UNIQUE, completed INTEGER NOT NULL DEFAULT 0)`).execute();
  app.db().newQuery(`CREATE TABLE drop_volume_deletions (
    node TEXT NOT NULL, nib TEXT NOT NULL, PRIMARY KEY(node,nib))`).execute();
}, () => { throw new Error("Restore a database snapshot to roll back."); });
