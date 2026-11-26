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
	Measured   int64  `db:"measured"`
	Billable   int64  `db:"billable"`
	At         int64  `db:"at"`
	Awake      bool   `db:"awake"`
	Units      int64  `db:"units"`
	Remainder  int64  `db:"remainder"`
	Node       string `db:"node"`
	Deployment string `db:"deployment"`
}

// Meter phase 1 against the runner observation timestamp. Arrival time
// would charge an identical HTTP retry twice. Retain the last observation
// across agent reconnects and control-plane restarts.
func meterReport(tx core.App, node string, app *core.Record, r dropapi.AppReport, at int64, apply bool) (bool, error) {
	if at <= 0 || r.DeploymentID == "" || r.DeploymentID != deploymentID(app) {
		return false, nil
	}
	awake := r.Phase == "running" || r.Phase == "starting"
	units := int64(app.GetInt("vcpu"))
	if app.GetString("beta_digest") != "" {
		units = int64(max(1, app.GetInt("memory_mib")/256))
	}
	var prev meterSample
	err := tx.DB().NewQuery("SELECT at,awake,units,remainder,node,deployment,measured,billable FROM drop_credit_meter WHERE app={:a}").Bind(dbx.Params{"a": app.Id}).One(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	same := prev.Node == node && prev.Deployment == r.DeploymentID
	if at <= prev.At {
		return false, nil
	}
	// Keep the counter baseline while reports coalesce. Always consume new
	// usage in a sleeping report; the VM may have woken between samples.
	counter := r.AwakeCounter
	if same && prev.Awake == awake && (counter == nil || counter.BillableSeconds == prev.Billable) && (!awake || at-prev.At < 60) {
		return false, nil
	}
	if same && prev.Awake == awake && awake && at-prev.At < 60 {
		return false, nil
	}
	if !apply {
		return true, nil
	}
	owner := app.GetString("owner")
	var seconds int64
	if same && prev.Awake {
		seconds = at - prev.At
	}
	measured, billable := prev.Measured, prev.Billable
	if !same || counter == nil {
		measured, billable = -1, -1
	}
	if counter != nil {
		if measured >= 0 && counter.MeasuredSeconds >= measured && counter.BillableSeconds >= billable {
			hostDelta := counter.BillableSeconds - billable
			_, err := tx.DB().NewQuery(`INSERT INTO drop_credit_comparison(node,app,deployment,hour,sampled,measured,billable)
   VALUES({:n},{:a},{:d},{:h},{:s},{:m},{:b}) ON CONFLICT(node,app,deployment,hour) DO UPDATE SET
   sampled=sampled+excluded.sampled,measured=measured+excluded.measured,billable=billable+excluded.billable`).
				Bind(dbx.Params{"n": node, "a": app.Id, "d": r.DeploymentID, "h": at / 3600 * 3600, "s": seconds, "m": counter.MeasuredSeconds - measured, "b": hostDelta}).Execute()
			if err != nil {
				return false, err
			}
			seconds = hostDelta
		} else if !same {
			// New deployments start their counter at zero. An older deployment
			// observed for the first time after upgrade establishes its baseline.
			if prev.At != 0 {
				seconds = counter.BillableSeconds
			}
		} else if measured >= 0 {
			return false, nil
		}
		measured, billable = counter.MeasuredSeconds, counter.BillableSeconds
	}
	if seconds > 0 {
		state, err := balanceOf(tx, owner)
		if err != nil {
			return false, err
		}
		switch {
		case state.Balance > 0:
			numerator := seconds*units*microcredits + prev.Remainder
			amount := min(state.Balance, numerator/3600)
			prev.Remainder = numerator % 3600
			if amount > 0 {
				source := fmt.Sprintf("compute:%s:%s:%d", app.Id, node, at)
				if err := appendCredit(tx, owner, app.Id, "compute", source, -amount); err != nil {
					return false, err
				}
				state.Balance -= amount
			}
		case !state.Paid:
			state.Trial = min(3600, state.Trial+seconds)
			if _, err := tx.DB().Update("drop_credit_accounts", dbx.Params{"trial_seconds": state.Trial}, dbx.HashExp{"owner": owner}).Execute(); err != nil {
				return false, err
			}
		}
		if creditBlocked(state) {
			awake = false
			if err := enforceCredits(tx, owner, state); err != nil {
				return false, err
			}
		}
	}
	_, err = tx.DB().NewQuery(`INSERT INTO drop_credit_meter(app,at,node,deployment,awake,units,remainder,measured,billable) VALUES({:a},{:t},{:n},{:d},{:w},{:u},{:r},{:m},{:b})
 ON CONFLICT(app) DO UPDATE SET at=excluded.at,node=excluded.node,deployment=excluded.deployment,awake=excluded.awake,units=excluded.units,remainder=excluded.remainder,measured=excluded.measured,billable=excluded.billable`).Bind(dbx.Params{"a": app.Id, "t": at, "n": node, "d": r.DeploymentID, "w": awake, "u": units, "r": prev.Remainder, "m": measured, "b": billable}).Execute()
	return true, err
}
