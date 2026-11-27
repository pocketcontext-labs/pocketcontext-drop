migrate(app => {
  const run = sql => app.db().newQuery(sql).execute();
  run("CREATE TABLE drop_official_config (id INTEGER PRIMARY KEY CHECK(id=1), enabled INTEGER NOT NULL DEFAULT 0)");
  run("INSERT INTO drop_official_config(id) VALUES(1)");
  run(`CREATE TABLE drop_official_releases (
    digest TEXT PRIMARY KEY, product TEXT NOT NULL CHECK(product IN ('crew','atlas')), version TEXT NOT NULL,
    size INTEGER NOT NULL, min_size INTEGER NOT NULL CHECK(min_size BETWEEN 1 AND 8), published INTEGER NOT NULL DEFAULT 0, UNIQUE(product,version))`);
  run(`CREATE TABLE drop_official_uploads (token_hash TEXT PRIMARY KEY, owner TEXT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    product TEXT NOT NULL, version TEXT NOT NULL, expires_at INTEGER NOT NULL)`);
  run(`CREATE TABLE drop_official_rehearsals(candidate TEXT NOT NULL REFERENCES drop_official_releases(digest),baseline TEXT NOT NULL,
    evidence TEXT NOT NULL, receipt TEXT NOT NULL, actor TEXT NOT NULL, PRIMARY KEY(candidate,baseline,evidence))`);
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.add(new TextField({name:"official_product", hidden:true, max:16}));
  apps.fields.add(new TextField({name:"official_target",hidden:true,max:64}));
  apps.fields.add(new TextField({name:"official_installed", hidden:true, max:64}));
  apps.fields.add(new TextField({name:"official_version", hidden:true, max:80}));
  apps.fields.add(new NumberField({name:"official_minimum",hidden:true,onlyInt:true,min:0,max:8}));
  app.save(apps);
}, () => { throw new Error("Restore the database backup to roll back."); });
