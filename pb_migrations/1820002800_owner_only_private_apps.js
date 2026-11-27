// Fence tickets already issued to testers when nodes apply their next desired revision.
migrate((app) => {
  app.db().newQuery("UPDATE drop_private_keys SET seed=randomblob(32) WHERE app IN (SELECT app FROM drop_private_testers)").execute();
  app.db().newQuery("DROP TABLE drop_private_testers").execute();
}, () => { throw new Error("Restore a database backup to roll back."); });
