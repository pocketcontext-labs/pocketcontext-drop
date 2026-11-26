migrate((app) => {
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new BoolField({name:"private_owner_email", hidden:true}));
  apps.fields.add(new BoolField({name:"required_s3", hidden:true}));
  app.save(apps);
  app.db().newQuery(`UPDATE apps SET required_s3=1 WHERE beta_digest IN (
    SELECT p.digest FROM drop_beta_probes p,
      json_each(CASE WHEN json_valid(p.manifest) THEN p.manifest ELSE '{}' END,'$.requires') f
    WHERE p.state='valid' AND f.value='s3_replica')`).execute();
}, () => { throw new Error("Restore the database backup to roll back."); });
