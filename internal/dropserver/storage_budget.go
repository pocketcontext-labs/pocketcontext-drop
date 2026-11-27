package dropserver

import (
	"database/sql"
	"errors"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/usage"
)

var budgetToken = regexp.MustCompile("^[0-9a-f]{32}$")

func serveStorageBudget(re *core.RequestEvent, state *agentState) error {
	node, err := agentNode(re, state)
	if err != nil {
		return writeError(re, err)
	}
	var in dropapi.StorageBudget
	body, err := io.ReadAll(http.MaxBytesReader(re.Response, re.Request.Body, 2048))
	if err != nil || decodeStrict(body, &in) != nil || in.Schema != dropapi.StorageBudgetSchema || !budgetToken.MatchString(in.Token) || !appspec.AppIDPattern.MatchString(in.App) || !appspec.DeploymentIDOK(in.App, in.Deployment) || in.Bytes < 0 || in.Bytes > dropapi.MaxHostObjectBytes {
		return writeError(re, Invalid("request", "Invalid storage reservation."))
	}
	err = re.App.RunInTransaction(func(tx core.App) error {
		fresh, err := tx.FindRecordById("nodes", node.Id)
		if err != nil || fresh.TokenKey() != node.TokenKey() || fresh.GetString("state") == dropapi.NodeDisabled {
			return Forbidden("Node access changed.")
		}
		return storageBudget(tx, node.Id, in)
	})
	if err != nil {
		return writeError(re, err)
	}
	return re.JSON(http.StatusOK, map[string]bool{"accepted": true})
}

type reservation struct {
	Expired bool  `db:"expired"`
	Paid    int64 `db:"paid"`

	App        string `db:"app"`
	Node       string `db:"node"`
	Deployment string `db:"deployment"`
	Class      string `db:"class"`
	Limit      int64  `db:"byte_limit"`
	Held       int64  `db:"held"`
	State      string `db:"state"`
	Markup     int64  `db:"markup"`
	Exchange   int64  `db:"exchange"`
	Price      int64  `db:"price"`
}

func (r *reservation) pricing() dropapi.CreditSettings {
	return dropapi.CreditSettings{MarkupPPM: r.Markup, EURPerUSDPPM: r.Exchange, EuroMicrosPerCredit: r.Price}
}

func storageHold(class string, bytes int64, s dropapi.CreditSettings) (int64, error) {
	c := usage.Counts{BytesOut: bytes}
	switch class {
	case "read":
		c.ClassB = 1
	case "write", "list":
		c.ClassA = 1
	case "delete":
		c.Deletes = 1
	default:
		return 0, Invalid("class", "Unknown storage request class.")
	}
	cost := storageCost(c, usage.Stored{}, 1, s)
	ceil := new(big.Int).Sub(cost.Denom(), big.NewInt(1))
	ceil.Add(ceil, cost.Num()).Quo(ceil, cost.Denom())
	if !ceil.IsInt64() {
		return 0, Invalid("bytes", "Request exceeds the storage budget.")
	}
	return ceil.Int64(), nil
}

func storageBudget(tx core.App, node string, in dropapi.StorageBudget) error {
	id := strings.TrimPrefix(in.App, "d-")
	var r reservation
	err := tx.DB().NewQuery("SELECT app,node,deployment,class,byte_limit,held,state,markup,exchange,price,created<unixepoch()-604800 AS expired FROM drop_storage_reservations WHERE token={:token}").Bind(dbx.Params{"token": in.Token}).One(&r)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if r.App != id || r.Node != node || r.Deployment != in.Deployment || r.Class != in.Class {
			return NotFound("Reservation")
		}
		// A grant is single-use. An ambiguous reserve reply must never authorize a second forward.
		if in.Action != "observed" || in.Bytes > r.Limit {
			return Conflict("storage_reservation", "Storage reservation already exists.")
		}
		if r.State != "pending" || r.Expired {
			return nil
		}
		amount, err := storageHold(in.Class, in.Bytes, r.pricing())
		if err != nil {
			return err
		}
		a, err := tx.FindRecordById("apps", id)
		if err != nil {
			return err
		}
		owner := a.GetString("owner")
		before, err := balanceOf(tx, owner)
		if err != nil {
			return err
		}
		_, err = tx.DB().Update("drop_storage_reservations", dbx.Params{"held": 0, "paid": amount, "state": "observed"}, dbx.HashExp{"token": in.Token}).Execute()
		if err != nil {
			return err
		}
		if amount > 0 {
			if err := appendCredit(tx, owner, id, "s3", "proxy-observed:"+in.Token, -amount); err != nil {
				return err
			}
		}
		return applyStorageSettlement(tx, owner, before)
	}
	if in.Action != "reserve" {
		return NotFound("Reservation")
	}
	a, err := tx.FindRecordById("apps", id)
	if err != nil {
		return NotFound("App")
	}
	var placed bool
	err = tx.DB().NewQuery("SELECT EXISTS(SELECT 1 FROM placements WHERE app={:a} AND node={:n})").Bind(dbx.Params{"a": id, "n": node}).Row(&placed)
	if err != nil {
		return err
	}
	if !placed || deploymentID(a) != in.Deployment || a.GetBool("archived") || a.GetString("desired_state") == "stopped" || a.GetString("crew_enrollment") == "" {
		return NotFound("Deployment")
	}
	ok, err := replicaEligible(tx, a)
	if err != nil {
		return err
	}
	owner := a.GetString("owner")
	wallet, err := balanceOf(tx, owner)
	if err != nil {
		return err
	}
	s, err := creditSettings(tx)
	if err != nil {
		return err
	}
	held, err := storageHold(in.Class, in.Bytes, s)
	if err != nil {
		return err
	}
	if !ok || wallet.Balance <= wallet.Reserved || held > wallet.Balance-wallet.Reserved {
		return Forbidden("The workspace has insufficient available storage credits.")
	}
	_, err = tx.DB().Insert("drop_storage_reservations", dbx.Params{"token": in.Token, "owner": owner, "app": id, "node": node, "deployment": in.Deployment, "class": in.Class, "byte_limit": in.Bytes, "held": held, "state": "pending", "markup": s.MarkupPPM, "exchange": s.EURPerUSDPPM, "price": s.EuroMicrosPerCredit}).Execute()
	return err
}

// Only a token issued for this app may offset its hourly access-log charge.
// A duplicate provider delivery releases and charges the reservation once.
func reconcileStorageBudget(tx core.App, id string, obs usage.BudgetUsage) (bool, error) {
	var r reservation
	err := tx.DB().NewQuery("SELECT state,paid,markup,exchange,price,created<unixepoch()-604800 AS expired FROM drop_storage_reservations WHERE token={:t} AND app={:a}").Bind(dbx.Params{"t": obs.Token, "a": id}).One(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.State == "settled" || r.State == "waived" {
		return true, nil
	}
	a, err := tx.FindRecordById("apps", id)
	if err != nil {
		return false, err
	}
	owner := a.GetString("owner")
	before, err := balanceOf(tx, owner)
	if err != nil {
		return false, err
	}
	if _, err := tx.DB().Update("drop_storage_reservations", dbx.Params{"held": 0, "state": "settled"}, dbx.HashExp{"token": obs.Token}).Execute(); err != nil {
		return false, err
	}
	if r.State == "pending" && r.Expired {
		return true, applyStorageSettlement(tx, owner, before)
	}
	if r.Paid > 0 {
		if err := appendCredit(tx, owner, id, "s3", "proxy-reconcile:"+obs.Token, r.Paid); err != nil {
			return false, err
		}
	}
	pricing := r.pricing()
	if err := chargeS3At(tx, id, "proxy:"+obs.Token, obs.Counts, usage.Stored{}, 1, &pricing); err != nil {
		return false, err
	}
	return true, applyStorageSettlement(tx, owner, before)
}

// Apply a billing transition to the final net wallet, including any unused
// portion of a hold that became spendable again.
func applyStorageSettlement(tx core.App, owner string, before creditAccount) error {
	after, err := balanceOf(tx, owner)
	if err != nil {
		return err
	}
	resume := creditBlocked(before) && !creditBlocked(after)
	if resume {
		if err := resumeCreditHolds(tx, owner); err != nil {
			return err
		}
	}
	if (before.Balance == 0) != (after.Balance == 0) {
		if err := changeStorageMode(tx, owner); err != nil {
			return err
		}
	} else if resume {
		if err := syncDesiredSeqs(tx); err != nil {
			return err
		}
	}
	if !creditBlocked(before) && creditBlocked(after) {
		return enforceCredits(tx, owner, after)
	}
	return nil
}

// An ambiguous exchange whose provider record never arrived cannot encumber
// future purchases forever. Waive it once and release any affected holds.
func expireStorageReservations(app core.App) error {
	var owners []string
	if err := app.DB().NewQuery("SELECT DISTINCT owner FROM drop_storage_reservations WHERE state='pending' AND created<unixepoch()-604800").Column(&owners); err != nil {
		return err
	}
	for _, owner := range owners {
		if err := app.RunInTransaction(func(tx core.App) error {
			before, err := balanceOf(tx, owner)
			if err != nil {
				return err
			}
			_, err = tx.DB().NewQuery("UPDATE drop_storage_reservations SET held=0,state='waived' WHERE owner={:o} AND state='pending' AND created<unixepoch()-604800").Bind(dbx.Params{"o": owner}).Execute()
			if err != nil {
				return err
			}
			return applyStorageSettlement(tx, owner, before)
		}); err != nil {
			return err
		}
	}
	return nil
}
