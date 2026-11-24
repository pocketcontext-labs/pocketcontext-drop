migrate((app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.getByName("http_port").min = 1;
  app.save(apps);
}, () => {
  throw new Error("Rollback requires restoring the database backup.");
});
