package dropserver

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

func creditBlocked(state creditAccount) bool {
	return state.Balance == 0 && (state.Paid || state.Trial >= 3600)
}
func allowCreditDeployment(app core.App, owner string) error {
	state, err := balanceOf(app, owner)
	if err != nil {
		return err
	}
	if creditBlocked(state) {
		return Conflict("credits_required", "Buy credits to start or deploy an app. Your data is retained.")
	}
	return nil
}

// A billing hold owns only the stop it created. A voluntarily archived or
// stopped application stays that way when the account is topped up.
func enforceCredits(tx core.App, owner string, state creditAccount) error {
	if !creditBlocked(state) {
		return nil
	}
	apps, err := tx.FindRecordsByFilter("apps", "owner={:o} && archived=false", "", 0, 0, dbx.Params{"o": owner})
	if err != nil {
		return err
	}
	for _, app := range apps {
		previous := app.GetString("desired_state")
		if state.Paid && previous == "stopped" {
			continue
		}
		reason := "balance"
		if !state.Paid {
			reason = "trial"
			app.Set("archived", true)
			app.Set("archived_at", time.Now().UTC())
		}
		_, err := tx.DB().NewQuery("INSERT OR IGNORE INTO drop_credit_holds(app,reason,previous_state) VALUES({:a},{:r},{:s})").Bind(dbx.Params{"a": app.Id, "r": reason, "s": previous}).Execute()
		if err != nil {
			return err
		}
		app.Set("desired_state", "stopped")
		if err := tx.Save(app); err != nil {
			return err
		}
	}
	return syncDesiredSeqs(tx)
}
func resumeCreditHolds(tx core.App, owner string) error {
	var holds []struct {
		App      string `db:"app"`
		Reason   string `db:"reason"`
		Previous string `db:"previous_state"`
	}
	err := tx.DB().NewQuery("SELECT h.* FROM drop_credit_holds h JOIN apps a ON a.id=h.app WHERE a.owner={:o}").Bind(dbx.Params{"o": owner}).All(&holds)
	if err != nil {
		return err
	}
	for _, hold := range holds {
		app, err := tx.FindRecordById("apps", hold.App)
		if err != nil {
			return err
		}
		if hold.Reason == "trial" {
			app.Set("archived", false)
			app.Set("archived_at", "")
		}
		app.Set("desired_state", hold.Previous)
		if err := bumpDeployment(app); err != nil {
			return err
		}
		if err := tx.Save(app); err != nil {
			return err
		}
		if _, err := tx.DB().Delete("drop_credit_holds", dbx.HashExp{"app": app.Id}).Execute(); err != nil {
			return err
		}
	}
	return nil
}

type meterSample struct {
	At       int64 `db:"at"`
	Awake    bool  `db:"awake"`
	Measured int64 `db:"measured"`
	Billable int64 `db:"billable"`
}
type creditExecution struct {
	Start  int64 `db:"started"`
	Units  int64 `db:"units"`
	Legacy bool  `db:"legacy"`
}

// Each node/execution owns its own baseline. Late final reports can settle
// a previous deployment at its archived rate without touching the current one.
func meterReport(tx core.App, node string, app *core.Record, r dropapi.AppReport, at int64, apply bool) (bool, error) {
	if at <= 0 || r.DeploymentID == "" {
		return false, nil
	}
	identity := dbx.Params{"a": app.Id, "n": node, "d": r.DeploymentID}
	var exec creditExecution
	err := tx.DB().NewQuery("SELECT started,units,legacy FROM drop_credit_executions WHERE app={:a} AND deployment={:d}").Bind(dbx.Params{"a": app.Id, "d": r.DeploymentID}).One(&exec)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var prev meterSample
	err = tx.DB().NewQuery("SELECT at,awake,measured,billable FROM drop_credit_meter WHERE app={:a} AND node={:n} AND deployment={:d}").Bind(identity).One(&prev)
	exists := err == nil
	if !exists && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if !exists && r.DeploymentID != deploymentID(app) {
		return false, nil
	}
	if at <= prev.At {
		return false, nil
	}
	awake := r.Phase == "running" || r.Phase == "starting"
	counter := r.AwakeCounter
	unchanged := counter == nil || counter.MeasuredSeconds == prev.Measured && counter.BillableSeconds == prev.Billable
	if exists && awake == prev.Awake && ((!awake && unchanged) || (awake && at-prev.At < 60)) {
		return false, nil
	}

	// One counter cannot advance by more than elapsed wall time. Allow one
	// second for whole-second sampling on independent clocks. Refuse a
	// regressed or impossible counter without advancing the durable baseline.
	since := prev.At
	if !exists {
		since = exec.Start
	}
	span := max(0, at-since)
	var seconds int64
	if prev.Awake {
		seconds = span
	}
	measured, billable := int64(-1), int64(-1)
	if counter != nil {
		measured, billable = counter.MeasuredSeconds, counter.BillableSeconds
		switch {
		case exists && prev.Measured >= 0:
			dm, db := measured-prev.Measured, billable-prev.Billable
			if dm < 0 || db < 0 || db > dm+1 || dm > span+1 {
				return false, nil
			}
			if apply && span > 0 {
				_, err = tx.DB().Insert("drop_credit_comparison", dbx.Params{"node": node, "app": app.Id, "deployment": r.DeploymentID, "since": since, "until": at, "sampled": seconds, "measured": dm, "billable": db}).Execute()
				if err != nil {
					return false, err
				}
			}
			seconds = db
		case !exists && !exec.Legacy:
			if measured > span+1 {
				return false, nil
			}
			seconds = billable
		}
	}
	if !apply {
		return true, nil
	}
	if seconds > 0 {
		blocked, err := settleCompute(tx, app, node, r.DeploymentID, at, seconds, exec.Units)
		if err != nil {
			return false, err
		}
		if blocked {
			awake = false
		}
	}
	_, err = tx.DB().NewQuery(`INSERT INTO drop_credit_meter(app,node,deployment,at,awake,units,measured,billable)
 VALUES({:a},{:n},{:d},{:t},{:w},{:u},{:m},{:b})
 ON CONFLICT(app,node,deployment) DO UPDATE SET at=excluded.at,awake=excluded.awake,measured=excluded.measured,billable=excluded.billable`).Bind(dbx.Params{"a": app.Id, "n": node, "d": r.DeploymentID, "t": at, "w": awake, "u": exec.Units, "m": measured, "b": billable}).Execute()
	return true, err
}

func settleCompute(tx core.App, app *core.Record, node, deployment string, at, seconds, units int64) (bool, error) {
	owner := app.GetString("owner")
	state, err := balanceOf(tx, owner)
	if err != nil {
		return false, err
	}
	switch {
	case state.Balance > 0:
		seconds = min(seconds, max(0, at-state.FundedAt))
		var rem int64
		if err := tx.DB().NewQuery("SELECT COALESCE((SELECT remainder FROM drop_credit_compute_carry WHERE app={:a}),0)").Bind(dbx.Params{"a": app.Id}).Row(&rem); err != nil {
			return false, err
		}
		// Saturate before multiplication. The wallet cap fits 10^12 microcredits
		// and units is constrained to 1..16 when the deployment is recorded.
		rate := units * microcredits
		seconds = min(seconds, (state.Balance*3600+rate-1)/rate)
		numerator := seconds*rate + rem
		amount := min(state.Balance, numerator/3600)
		if _, err := tx.DB().NewQuery("INSERT INTO drop_credit_compute_carry(app,remainder) VALUES({:a},{:r}) ON CONFLICT(app) DO UPDATE SET remainder=excluded.remainder").Bind(dbx.Params{"a": app.Id, "r": numerator % 3600}).Execute(); err != nil {
			return false, err
		}
		if amount > 0 {
			key := fmt.Sprintf("compute:%s:%s:%d", deployment, node, at)
			if err := appendCredit(tx, owner, app.Id, "compute", key, -amount); err != nil {
				return false, err
			}
			state.Balance -= amount
		}
	case !state.Paid:
		state.Trial += min(seconds, 3600-state.Trial)
		if _, err := tx.DB().Update("drop_credit_accounts", dbx.Params{"trial_seconds": state.Trial}, dbx.HashExp{"owner": owner}).Execute(); err != nil {
			return false, err
		}
	}
	if creditBlocked(state) {
		return true, enforceCredits(tx, owner, state)
	}
	return false, nil
}
