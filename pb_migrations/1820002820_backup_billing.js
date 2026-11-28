// Start retained-byte billing only after Drop has verified the stored archive.
// Each archive retains its quoted rate for its seven-day lifetime.
migrate((app) => {
  const run = sql => app.db().newQuery(sql).execute();
  run(`CREATE TABLE drop_backup_meter (
    backup TEXT PRIMARY KEY REFERENCES drop_backups(id) ON DELETE CASCADE,
    ready_at INTEGER NOT NULL, through INTEGER NOT NULL, markup INTEGER NOT NULL, exchange INTEGER NOT NULL, price INTEGER NOT NULL)`);
  run("CREATE INDEX drop_backup_meter_due ON drop_backup_meter(through)");
  run("CREATE TABLE drop_backup_carry(owner TEXT PRIMARY KEY, fraction TEXT NOT NULL)");
  run(`CREATE TRIGGER drop_backup_begin_billing AFTER UPDATE OF state ON drop_backups
    WHEN NEW.state='ready' AND OLD.state='pending' BEGIN
    INSERT INTO drop_backup_meter SELECT NEW.id,unixepoch(),unixepoch(),markup_ppm,eur_per_usd_ppm,euro_micros_per_credit FROM drop_credit_settings WHERE id=1;
    END`);
  run(`INSERT INTO drop_backup_meter SELECT b.id,unixepoch(),unixepoch(),s.markup_ppm,s.eur_per_usd_ppm,s.euro_micros_per_credit
    FROM drop_backups b CROSS JOIN drop_credit_settings s WHERE b.state='ready' AND s.id=1`);
}, () => { throw new Error("Restore the control-plane database backup to roll back."); });
