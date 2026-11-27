migrate(app => {
  const run = sql => app.db().newQuery(sql).execute();
  run("CREATE TABLE drop_official_products (product TEXT PRIMARY KEY, prefix TEXT NOT NULL UNIQUE CHECK(prefix != 'dev' AND prefix NOT LIKE 'dev-%'))");
  run("INSERT INTO drop_official_products VALUES ('crew','crew'), ('atlas','atlas')");

  // Preserve evidence before rebuilding the referenced release table.
  run("CREATE TEMP TABLE saved_rehearsals AS SELECT * FROM drop_official_rehearsals");
  run("DROP TABLE drop_official_rehearsals");
  run(`CREATE TABLE catalog_releases_new (
    digest TEXT PRIMARY KEY, product TEXT NOT NULL, version TEXT NOT NULL,
    size INTEGER NOT NULL, min_size INTEGER NOT NULL CHECK(min_size BETWEEN 1 AND 8),
    published INTEGER NOT NULL DEFAULT 0, archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)), UNIQUE(product,version))`);
  run("INSERT INTO catalog_releases_new(rowid,digest,product,version,size,min_size,published,archived) SELECT rowid,digest,product,version,size,min_size,published,archived FROM drop_official_releases");
  run("DROP TABLE drop_official_releases");
  run("ALTER TABLE catalog_releases_new RENAME TO drop_official_releases");
  run(`CREATE TABLE drop_official_rehearsals(candidate TEXT NOT NULL REFERENCES drop_official_releases(digest),baseline TEXT NOT NULL,
    evidence TEXT NOT NULL, receipt TEXT NOT NULL, actor TEXT NOT NULL, PRIMARY KEY(candidate,baseline,evidence))`);
  run("INSERT INTO drop_official_rehearsals SELECT * FROM saved_rehearsals");
  run("DROP TABLE saved_rehearsals");

  run(`CREATE TABLE catalog_submissions_new (
    release TEXT PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,
    submitted_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    product TEXT NOT NULL, version TEXT NOT NULL, digest TEXT NOT NULL,
    disclosure_approved_by TEXT NOT NULL DEFAULT '',
    submitted_at INTEGER NOT NULL DEFAULT (unixepoch()),
    accepted INTEGER NOT NULL DEFAULT 0 CHECK(accepted IN (0,1)))`);
  run("INSERT INTO catalog_submissions_new SELECT * FROM drop_catalog_submissions");
  run("DROP TABLE drop_catalog_submissions");
  run("ALTER TABLE catalog_submissions_new RENAME TO drop_catalog_submissions");
  const apps = app.findCollectionByNameOrId("apps");
  apps.fields.getByName("official_product").max = 32;
  app.save(apps);
}, () => { throw new Error("Restore the database backup to roll back catalog prefixes."); });
