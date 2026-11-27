// Keep existing billing holds stopped until their owner's next top-up.
migrate((app) => {
  app.db().newQuery(`UPDATE drop_credit_accounts SET trial_seconds=3600
    WHERE balance=0 AND EXISTS(SELECT 1 FROM drop_credit_holds h JOIN apps a ON a.id=h.app WHERE a.owner=drop_credit_accounts.owner)`).execute();
  app.db().newQuery("UPDATE drop_credit_accounts SET trial_seconds=0 WHERE paid=1 AND balance>0").execute();
}, () => { throw new Error("Restore the pre-upgrade backup to roll back shared credit buffers."); });
