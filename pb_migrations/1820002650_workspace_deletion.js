// Keep pending cleanup records available to the node reconciler only.
migrate((app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new BoolField({ name: "deleting", hidden: true }));
  apps.listRule = "(" + apps.listRule + ") && deleting = false";
  apps.viewRule = "(" + apps.viewRule + ") && deleting = false";
  app.save(apps);
  app.db().newQuery("UPDATE apps SET deleting = true WHERE id IN (SELECT app FROM drop_app_deletions)").execute();
}, () => { throw new Error("Restore the database backup to roll back."); });
