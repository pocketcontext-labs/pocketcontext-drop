// Developer accounts retain their ids, apps and billing. An admin-owned
// private app requires an explicit ownership decision before upgrading.
migrate((app) => {
  const owned = app.findRecordsByFilter("apps", "developer_private = true && owner.role = 'admin'", "", 1, 0);
  if (owned.length) {
    throw new Error("A private app belongs to an admin. Resolve its ownership under a separate developer account and retry the migration.");
  }
  // Fence previously issued gateway tickets. The next desired-document pull
  // reconciles the changed verification key by its content hash.
  app.db().newQuery("UPDATE drop_private_keys SET seed=randomblob(32) WHERE app IN (SELECT t.app FROM drop_private_testers t JOIN users u ON u.id=t.account WHERE u.role='admin')").execute();
  app.db().newQuery("DELETE FROM drop_private_testers WHERE account IN (SELECT id FROM users WHERE role='admin')").execute();
  for (const collection of ["users", "invites"]) {
    const c = app.findCollectionByNameOrId(collection);
    c.fields.getByName("role").values = ["user", "developer", "admin"];
    app.save(c);
  }
  app.db().newQuery("UPDATE users SET role = 'developer' WHERE role = 'user' AND developer = 1").execute();
  const users = app.findCollectionByNameOrId("users");
  users.fields.removeByName("developer");
  app.save(users);
}, () => { throw new Error("Restore the database backup to roll back."); });
