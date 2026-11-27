// Release provenance is owner-readable through the existing per-app release rules.
migrate((app)=>{
  const releases=app.findCollectionByNameOrId("releases");
  releases.fields.add(new TextField({name:"archive_sha256",max:64}));
  releases.fields.add(new TextField({name:"executable_sha256",max:64}));
  releases.fields.add(new TextField({name:"executable_path",max:1024}));
  app.save(releases);
},()=>{throw new Error("Restore a backup to roll back.");});
