// Host services usage (internal/dropserver/host_usage.go): the node's S3
// proxy reports each app deployment's monotonic counters with a sequence
// number. node_reports keeps the last deployment, sequence and counters taken
// and the app's running total of the differences, so a repeated or older
// report never counts twice. Usage alone is written at most every five
// minutes (heartbeat.go's rule); the counters are cumulative, so nothing is
// lost in between. Rules are node_reports' own (owner's apps,
// admins all; no record writes).
migrate((app) => {
  const reports = app.findCollectionByNameOrId("node_reports");
  reports.fields.add(new TextField({ name: "host_usage_deployment", max: 64 }));
  reports.fields.add(new NumberField({ name: "host_usage_seq", min: 0, onlyInt: true }));
  reports.fields.add(new JSONField({ name: "host_usage", maxSize: 1024 }));
  reports.fields.add(new JSONField({ name: "host_usage_total", maxSize: 1024 }));
  reports.fields.add(new DateField({ name: "host_usage_at" }));
  app.save(reports);
}, () => {
  throw new Error("Rollback requires restoring the database backup.");
});
