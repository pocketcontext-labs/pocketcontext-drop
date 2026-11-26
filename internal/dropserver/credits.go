package dropserver

import (
	"fmt"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

const microcredits int64 = 1_000_000

func init() {
	register("credits.accounts", creditsAccounts)
	register("credits.get", creditsGet)
	register("credits.buy", creditsBuy)
	register("credits.adjust", creditsAdjust)
	register("credits.configure", creditsConfigure)
}

func creditSettings(app core.App) (dropapi.CreditSettings, error) {
	var s dropapi.CreditSettings
	err := app.DB().NewQuery("SELECT euro_micros_per_credit,pack,cap,starting,markup_ppm,eur_per_usd_ppm FROM drop_credit_settings WHERE id=1").One(&s)
	return s, err
}

type creditAccount struct {
	FundedAt int64 `db:"funded_at" json:"-"`
	Balance  int64 `db:"balance" json:"balance"`
	Paid     bool  `db:"paid" json:"paid"`
	Trial    int64 `db:"trial_seconds" json:"trial_seconds"`
}

func balanceOf(app core.App, owner string) (creditAccount, error) {
	var a creditAccount
	err := app.DB().NewQuery("SELECT balance,paid,trial_seconds,funded_at FROM drop_credit_accounts WHERE owner={:o}").Bind(dbx.Params{"o": owner}).One(&a)
	return a, err
}

type creditEntry struct {
	ID      int64  `db:"id" json:"id"`
	Kind    string `db:"kind" json:"kind"`
	Delta   int64  `db:"delta" json:"delta"`
	App     string `db:"app" json:"app"`
	Created int64  `db:"created" json:"created"`
}
type creditApp struct {
	ID      string `db:"id" json:"id"`
	Name    string `db:"name" json:"name"`
	Rate    int64  `db:"rate" json:"rate"`
	Compute int64  `db:"compute" json:"compute"`
	S3      int64  `db:"s3" json:"s3"`
}

func creditsGet(c *Ctx, in *dropapi.CreditViewInput) (any, error) {
	owner := in.Account
	if owner == "" {
		owner = c.Auth.Id
	}
	if owner != c.Auth.Id && !c.Admin {
		return nil, NotFound("Account")
	}
	if _, err := c.App.FindRecordById("users", owner); err != nil {
		return nil, NotFound("Account")
	}
	state, err := balanceOf(c.App, owner)
	if err != nil {
		return nil, err
	}
	s, err := creditSettings(c.App)
	if err != nil {
		return nil, err
	}
	entries := []creditEntry{}
	params := dbx.Params{"o": owner, "before": in.Before}
	err = c.App.DB().NewQuery("SELECT id,kind,delta,app,created FROM drop_credit_ledger WHERE owner={:o} AND ({:before}=0 OR id<{:before}) ORDER BY id DESC LIMIT 100").Bind(params).All(&entries)
	if err != nil {
		return nil, err
	}
	apps := []creditApp{}
	err = c.App.DB().NewQuery(`SELECT a.id,a.name,
 CASE WHEN m.awake=1 AND a.archived=0 AND a.desired_state<>'stopped' THEN m.units ELSE 0 END AS rate,
 COALESCE(-SUM(CASE WHEN l.kind='compute' THEN l.delta END),0) AS compute,
 COALESCE(-SUM(CASE WHEN l.kind='s3' THEN l.delta END),0) AS s3
 FROM apps a LEFT JOIN placements p ON p.app=a.id LEFT JOIN drop_credit_meter m ON m.app=a.id AND m.node=p.node AND m.deployment=a.nib_app_id||'-dep-'||CAST(a.deployment_seq AS INTEGER) LEFT JOIN drop_credit_ledger l ON l.app=a.id AND l.owner=a.owner
 WHERE a.owner={:o} GROUP BY a.id ORDER BY a.name,a.id`).Bind(dbx.Params{"o": owner}).All(&apps)
	if err != nil {
		return nil, err
	}
	return map[string]any{"account": owner, "state": state, "settings": s, "entries": entries, "apps": apps}, nil
}
func creditsConfigure(c *Ctx, s *dropapi.CreditSettings) (any, error) {
	if s.Pack < 1 || s.Pack > s.Cap || s.Cap > 1_000_000 || s.Starting < 0 || s.Starting > s.Cap || s.EuroMicrosPerCredit < 1 || s.EuroMicrosPerCredit > 1_000_000 || s.MarkupPPM < 1 || s.MarkupPPM > 10_000_000 || s.EURPerUSDPPM < 1 || s.EURPerUSDPPM > 10_000_000 {
		return nil, Invalid("settings", "Choose positive credit values and a pack and initial grant within the cap.")
	}
	_, err := c.App.DB().Update("drop_credit_settings", dbx.Params{"pack": s.Pack, "cap": s.Cap, "starting": s.Starting, "markup_ppm": s.MarkupPPM, "eur_per_usd_ppm": s.EURPerUSDPPM, "euro_micros_per_credit": s.EuroMicrosPerCredit}, dbx.HashExp{"id": 1}).Execute()
	return s, err
}
func creditsBuy(c *Ctx, _ *dropapi.MeInput) (any, error) {
	s, err := creditSettings(c.App)
	if err != nil {
		return nil, err
	}
	return creditChange(c, c.Auth.Id, "purchase", s.Pack*microcredits, s.Cap*microcredits)
}
func creditsAdjust(c *Ctx, in *dropapi.CreditAdjustment) (any, error) {
	if _, err := c.App.FindRecordById("users", in.Account); err != nil {
		return nil, NotFound("Account")
	}
	s, err := creditSettings(c.App)
	if err != nil {
		return nil, err
	}
	return creditChange(c, in.Account, "adjustment", in.Microcredits, s.Cap*microcredits)
}
func creditChange(c *Ctx, owner, kind string, delta, cap int64) (any, error) {
	if c.creditKey == "" {
		return nil, Invalid("idempotency_key", "Supply a unique idempotency key for this transaction.")
	}
	state, err := balanceOf(c.App, owner)
	if err != nil {
		return nil, err
	}
	var found int
	if err := c.App.DB().NewQuery("SELECT COUNT(*) FROM drop_credit_ledger WHERE source={:s}").Bind(dbx.Params{"s": c.creditKey}).Row(&found); err != nil {
		return nil, err
	}
	if found > 0 {
		return nil, Conflict(dropapi.CodeIdempotencyConflict, "This credit transaction was already applied.")
	}
	if delta == 0 || delta < -state.Balance {
		return nil, Invalid("microcredits", "The adjustment must change the balance and leave it at or above zero.")
	}
	if delta > 0 && delta > cap-state.Balance {
		return nil, Conflict("credit_cap", fmt.Sprintf("This transaction would exceed the balance cap of %d credits.", cap/microcredits))
	}
	if err := appendCredit(c.App, owner, "", kind, c.creditKey, delta); err != nil {
		return nil, err
	}
	if state.Balance == 0 && delta > 0 {
		// Missing reports can straddle a purchase. Do not charge the newly
		// funded account for any runtime before this transaction.
		_, err := c.App.DB().Update("drop_credit_accounts", dbx.Params{"funded_at": agentStateOf(c.App).clock().Unix()}, dbx.HashExp{"owner": owner}).Execute()
		if err != nil {
			return nil, err
		}
		// Running trial deployments keep their current local-storage contract until redeployed.
		_, err = c.App.DB().NewQuery("INSERT OR REPLACE INTO drop_credit_local_deploy(app,seq) SELECT id,deployment_seq FROM apps WHERE owner={:o} AND current_release<>''").Bind(dbx.Params{"o": owner}).Execute()
		if err != nil {
			return nil, err
		}
		if err := resumeCreditHolds(c.App, owner); err != nil {
			return nil, err
		}
	}
	if state.Balance+delta == 0 {
		state.Balance = 0
		if err := enforceCredits(c.App, owner, state); err != nil {
			return nil, err
		}
	}
	c.Event(Event{SubjectUser: owner, Detail: map[string]any{"microcredits": delta}})
	return balanceOf(c.App, owner)
}
func appendCredit(tx core.App, owner, app, kind, source string, delta int64) error {
	_, err := tx.DB().Insert("drop_credit_ledger", dbx.Params{"owner": owner, "app": app, "kind": kind, "source": source, "delta": delta}).Execute()
	return err
}

func creditsAccounts(c *Ctx, in *dropapi.UserListInput) (any, error) {
	limit, offset, err := page(in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	type summary struct {
		ID        string `db:"id" json:"id"`
		Email     string `db:"email" json:"email"`
		Balance   int64  `db:"balance" json:"balance"`
		Purchases int64  `db:"purchases" json:"purchases"`
	}
	rows := []summary{}
	err = c.App.DB().NewQuery(`SELECT u.id,u.email,c.balance,COALESCE(sum(l.delta),0) AS purchases FROM users u JOIN drop_credit_accounts c ON c.owner=u.id LEFT JOIN drop_credit_ledger l ON l.owner=u.id AND l.kind='purchase' GROUP BY u.id ORDER BY u.email,u.id LIMIT {:l} OFFSET {:o}`).Bind(dbx.Params{"l": limit + 1, "o": offset}).All(&rows)
	if err != nil {
		return nil, err
	}
	return map[string]any{"accounts": rows[:min(limit, len(rows))], "has_more": len(rows) > limit}, nil
}
