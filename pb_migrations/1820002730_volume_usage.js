migrate((app) => {
  const reports = app.findCollectionByNameOrId("node_reports");
  reports.fields.add(new JSONField({ name: "volume_usage", maxSize: 256 }));
  app.save(reports);
}, () => { throw new Error("Restore the database backup to roll back."); });
