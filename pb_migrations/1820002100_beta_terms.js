migrate((app) => {
  const users = app.findCollectionByNameOrId("users");
  users.fields.add(new BoolField({ name: "beta_terms_pending", hidden: true }));
  users.fields.add(new TextField({ name: "beta_terms_version", max: 80, hidden: true }));
  users.fields.add(new DateField({ name: "beta_terms_accepted_at", hidden: true }));
  app.save(users);
}, () => {
  throw new Error("Rollback requires restoring the database backup.");
});
