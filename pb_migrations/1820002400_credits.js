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
    paid INTEGER NOT NULL DEFAULT 0, trial_seconds INTEGER NOT NULL DEFAULT 0 CHECK(trial_seconds BETWEEN 0 AND 3600))`);
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
  run(`CREATE TABLE drop_credit_meter(app TEXT PRIMARY KEY,node TEXT NOT NULL,deployment TEXT NOT NULL,
    measured INTEGER NOT NULL DEFAULT -1,billable INTEGER NOT NULL DEFAULT -1,
    at INTEGER NOT NULL, awake INTEGER NOT NULL,units INTEGER NOT NULL,remainder INTEGER NOT NULL DEFAULT 0)`);
  run(`CREATE TABLE drop_credit_comparison(node TEXT NOT NULL, app TEXT NOT NULL, deployment TEXT NOT NULL, hour INTEGER NOT NULL,
    sampled INTEGER NOT NULL, measured INTEGER NOT NULL,billable INTEGER NOT NULL, PRIMARY KEY(node,app,deployment,hour))`);
  run("CREATE TABLE drop_credit_local_deploy(app TEXT PRIMARY KEY,seq INTEGER NOT NULL)");
  run("CREATE TABLE drop_credit_holds(app TEXT PRIMARY KEY, reason TEXT NOT NULL, previous_state TEXT NOT NULL)");
}, () => { throw new Error("Restore the pre-upgrade backup to roll back credit accounting."); });
