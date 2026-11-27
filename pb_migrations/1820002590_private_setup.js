migrate((app) => {
  const reports = app.findCollectionByNameOrId("node_reports");
  reports.fields.add(new JSONField({name: "first_run", maxSize: 1536, hidden: true}));
  app.save(reports);
}, () => { throw new Error("Restore the database backup to roll back."); });
