// Only committed secret removals may delete a parameter. This private work
// queue survives restarts and contains parameter names only.
migrate((app) => {
  app.db().newQuery("CREATE TABLE drop_secret_deletions (parameter TEXT PRIMARY KEY NOT NULL)").execute();
  // An upload's tenant-visible audit record may describe the app's release
  // but must not disclose whether another tenant had already stored its bytes.
  app.db().newQuery("UPDATE events SET detail = json_remove(detail, '$.stored') WHERE action = 'release.upload' AND json_type(detail, '$.stored') IS NOT NULL").execute();
}, () => {
  throw new Error("Rollback requires restoring the database backup.");
});
