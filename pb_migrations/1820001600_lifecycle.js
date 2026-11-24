migrate((app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new NumberField({ name: "hook_port", min: 0, max: 65535, onlyInt: true }));
  apps.fields.add(new SelectField({ name: "crew_enrollment", values: ["fresh", "established"], maxSelect: 1 }));
  app.save(apps);
  const reports = app.findCollectionByNameOrId("node_reports");
  reports.fields.add(new JSONField({ name: "last_hook", maxSize: 512 }));
  app.save(reports);
}, () => {
  throw new Error("Rollback requires restoring the database backup.");
});
