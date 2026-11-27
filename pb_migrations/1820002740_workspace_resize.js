migrate((app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new JSONField({ name: "local_resize", maxSize: 1024, hidden: true }));
  app.save(apps);
}, () => { throw new Error("Restore the database backup to roll back."); });
