migrate(app => {
  const run = sql => app.db().newQuery(sql).execute();
  run("CREATE TABLE drop_source_submissions(commit_url TEXT PRIMARY KEY, submitted_by TEXT NOT NULL, submitted_at INTEGER NOT NULL DEFAULT(unixepoch()))");
  run("DELETE FROM drop_official_uploads");
  run("ALTER TABLE drop_official_uploads ADD COLUMN commit_url TEXT NOT NULL DEFAULT ''");
  // Preserve historical candidates and their receipts, including their foreign keys.
  run("ALTER TABLE drop_official_releases RENAME TO drop_legacy_catalog_releases");
  run(`CREATE TABLE drop_official_releases(digest TEXT PRIMARY KEY,product TEXT NOT NULL,version TEXT NOT NULL,
    size INTEGER NOT NULL,min_size INTEGER NOT NULL CHECK(min_size BETWEEN 1 AND 8),published INTEGER NOT NULL DEFAULT 0,
    archived INTEGER NOT NULL DEFAULT 0, commit_url TEXT NOT NULL DEFAULT '', built_by TEXT NOT NULL DEFAULT '', UNIQUE(product,version))`);
  run(`INSERT INTO drop_official_releases(digest,product,version,size,min_size,published,archived)
    SELECT digest,product,version,size,min_size,published,archived FROM drop_legacy_catalog_releases WHERE published>0`);
  run("CREATE TABLE drop_upgrade_sources(candidate TEXT NOT NULL REFERENCES drop_official_releases(digest), baseline TEXT NOT NULL REFERENCES drop_official_releases(digest), PRIMARY KEY(candidate,baseline))");
  run(`INSERT OR IGNORE INTO drop_upgrade_sources SELECT h.candidate,h.baseline FROM drop_official_rehearsals h
    JOIN drop_official_releases c ON c.digest=h.candidate JOIN drop_official_releases b ON b.digest=h.baseline`);
}, () => {throw new Error("Restore the pre-upgrade backup to roll back catalog submissions.");});
