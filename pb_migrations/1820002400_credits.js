// Private accounting tables. A ledger entry and its materialized balance
// always commit together, including the first grant for a new account.
migrate((app) => {
  const run = sql => app.db().newQuery(sql).execute();
  run(`CREATE TABLE drop_credit_settings (
    id INTEGER PRIMARY KEY CHECK(id=1), euro_micros_per_credit INTEGER NOT NULL CHECK(euro_micros_per_credit BETWEEN 1 AND 1000000),
    pack INTEGER NOT NULL CHECK(pack BETWEEN 1 AND cap), cap INTEGER NOT NULL CHECK(cap BETWEEN 1 AND 1000000),
    starting INTEGER NOT NULL CHECK(starting BETWEEN 0 AND cap), markup_ppm INTEGER NOT NULL CHECK(markup_ppm BETWEEN 1 AND 10000000),
    eur_per_usd_ppm INTEGER NOT NULL CHECK(eur_per_usd_ppm BETWEEN 1 AND 10000000))`);
  run("INSERT INTO drop_credit_settings VALUES(1,10000,500,10000,0,1250000,1000000)");
  run(`CREATE TABLE drop_credit_accounts (
    owner TEXT PRIMARY KEY, balance INTEGER NOT NULL DEFAULT 0 CHECK(balance >= 0),
    funded_at INTEGER NOT NULL DEFAULT 0, paid INTEGER NOT NULL DEFAULT 0, trial_seconds INTEGER NOT NULL DEFAULT 0 CHECK(trial_seconds BETWEEN 0 AND 3600))`);
  run(`CREATE TABLE drop_credit_ledger (
    id INTEGER PRIMARY KEY, owner TEXT NOT NULL, app TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL,
    delta INTEGER NOT NULL, source TEXT NOT NULL UNIQUE, created INTEGER NOT NULL DEFAULT(unixepoch()))`);
  run("CREATE TRIGGER drop_credit_no_update BEFORE UPDATE ON drop_credit_ledger BEGIN SELECT RAISE(ABORT,'append-only ledger'); END");
  run("CREATE TRIGGER drop_credit_no_delete BEFORE DELETE ON drop_credit_ledger BEGIN SELECT RAISE(ABORT,'append-only ledger'); END");
  run("CREATE TABLE drop_credit_s3_receipts(source TEXT PRIMARY KEY)");
  run("CREATE TABLE drop_credit_s3_carry(app TEXT PRIMARY KEY,fraction TEXT NOT NULL)");
  run("CREATE INDEX drop_credit_history ON drop_credit_ledger(owner,id)");
  run(`CREATE TRIGGER drop_credit_apply AFTER INSERT ON drop_credit_ledger BEGIN
     UPDATE drop_credit_accounts SET balance=balance+NEW.delta,paid=MAX(paid,NEW.delta>0) WHERE owner=NEW.owner;
     SELECT CASE WHEN changes()<>1 THEN RAISE(ABORT,'missing credit account') END; END`);
  run(`CREATE TRIGGER drop_credit_create AFTER INSERT ON users BEGIN
    INSERT INTO drop_credit_accounts(owner) VALUES(NEW.id);
    INSERT INTO drop_credit_ledger(owner,kind,delta,source) SELECT NEW.id,'adjustment',starting*1000000,'initial:'||NEW.id FROM drop_credit_settings WHERE starting>0;
    END`);
  run("INSERT INTO drop_credit_accounts(owner) SELECT id FROM users");
  run(`CREATE TABLE drop_credit_executions(app TEXT NOT NULL, deployment TEXT NOT NULL, units INTEGER NOT NULL CHECK(units BETWEEN 1 AND 16),
    started INTEGER NOT NULL DEFAULT(unixepoch()), legacy INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(app,deployment))`);
  run(`INSERT INTO drop_credit_executions(app,deployment,units,legacy)
    SELECT id,nib_app_id||'-dep-'||CAST(deployment_seq AS INTEGER),CASE WHEN beta_digest<>'' THEN max(1,CAST(memory_mib/256 AS INTEGER)) ELSE vcpu END,1 FROM apps WHERE current_release<>''`);
  for(const event of ["INSERT","UPDATE OF deployment_seq,current_release"]){
    const trigger = event === "INSERT" ? "drop_credit_execution_create" : "drop_credit_execution_update";
    run(`CREATE TRIGGER ${trigger} AFTER ${event} ON apps WHEN NEW.current_release<>'' BEGIN
      INSERT OR IGNORE INTO drop_credit_executions(app,deployment,units)
      VALUES(NEW.id,NEW.nib_app_id||'-dep-'||CAST(NEW.deployment_seq AS INTEGER),
        CASE WHEN NEW.beta_digest<>'' THEN max(1,CAST(NEW.memory_mib/256 AS INTEGER)) ELSE NEW.vcpu END);
      END`);
  }
  // NibRunner applies the changed guest storage environment on a new deployment.
  const identityNames="'AWS_ROLE_ARN','AWS_WEB_IDENTITY_TOKEN_FILE','AWS_ROLE_SESSION_NAME','AWS_WORKLOAD_IDENTITY'";
  run(`UPDATE apps SET deployment_seq=deployment_seq+1 WHERE current_release<>'' AND (
    crew_enrollment<>'' OR EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(apps.env) THEN apps.env ELSE '{}' END) WHERE key IN (${identityNames}))
    OR EXISTS(SELECT 1 FROM app_secret_names s WHERE s.app=apps.id AND s.name IN (${identityNames})))`);
  run(`CREATE TABLE drop_credit_meter(app TEXT NOT NULL,node TEXT NOT NULL,deployment TEXT NOT NULL,
    measured INTEGER NOT NULL DEFAULT -1,billable INTEGER NOT NULL DEFAULT -1,
    at INTEGER NOT NULL, awake INTEGER NOT NULL,units INTEGER NOT NULL,PRIMARY KEY(app,node,deployment))`);
  run("CREATE TABLE drop_credit_compute_carry(app TEXT PRIMARY KEY,remainder INTEGER NOT NULL)");
  run(`CREATE TABLE drop_credit_comparison(node TEXT NOT NULL, app TEXT NOT NULL, deployment TEXT NOT NULL, since INTEGER NOT NULL, until INTEGER NOT NULL,
    sampled INTEGER NOT NULL, measured INTEGER NOT NULL,billable INTEGER NOT NULL, PRIMARY KEY(node,app,deployment,until))`);
  run("CREATE TABLE drop_credit_local_deploy(app TEXT PRIMARY KEY,seq INTEGER NOT NULL)");
  run("CREATE TABLE drop_credit_holds(app TEXT PRIMARY KEY, reason TEXT NOT NULL, previous_state TEXT NOT NULL)");
}, () => { throw new Error("Restore the pre-upgrade backup to roll back credit accounting."); });
