// A Crew app keeps the verified address of its creator for its entire lifetime.
// Earlier apps have an empty address and receive no delivery capability.
migrate((app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new TextField({ name: "crew_owner_email", max: 254 }));
  apps.fields.add(new BoolField({ name: "sign_in_email_enabled" }));
  app.save(apps);
}, () => {
  throw new Error("Rollback requires restoring the database backup.");
});
