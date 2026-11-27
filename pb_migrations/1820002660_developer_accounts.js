// Developer access belongs to an account ID and is granted explicitly.
migrate((app) => {
  const users = app.findCollectionByNameOrId("users");
  users.fields.add(new BoolField({name: "developer"}));
  app.save(users);
}, () => { throw new Error("Restore the database backup to roll back."); });
