migrate((app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new NumberField({ name: "billing_deferred_seq", onlyInt: true, min: 0, hidden: true }));
  app.save(apps);
}, () => { throw new Error("Restore the database backup to roll back."); });
