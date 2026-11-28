migrate((app) => {
  const run = sql => app.db().newQuery(sql).execute();
  run("DROP TRIGGER drop_credit_create");
  run(`CREATE TRIGGER drop_credit_create AFTER INSERT ON users
    WHEN NEW.role IN ('user','developer') OR NEW.email='account@drop.invalid' BEGIN
      INSERT INTO drop_credit_accounts(owner) VALUES(NEW.id);
      INSERT INTO drop_credit_ledger(owner,kind,delta,source)
        SELECT NEW.id,'adjustment',starting*1000000,'initial:'||NEW.id FROM drop_credit_settings WHERE starting>0;
    END`);
  // Preserve legacy settlement state for existing workloads and accounting history.
  run(`DELETE FROM drop_credit_accounts WHERE owner IN (SELECT id FROM users WHERE role='admin' AND email!='account@drop.invalid')
    AND balance=0 AND NOT EXISTS (SELECT 1 FROM apps WHERE apps.owner=drop_credit_accounts.owner)
    AND NOT EXISTS (SELECT 1 FROM drop_credit_ledger WHERE drop_credit_ledger.owner=drop_credit_accounts.owner)`);
}, () => { throw new Error("Restore the pre-upgrade backup."); });
