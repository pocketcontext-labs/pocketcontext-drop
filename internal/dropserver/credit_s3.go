package dropserver

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
	"github.com/pocketcontext-labs/pocketcontext-drop/internal/usage"
)

// S3 Standard in eu-north-1. Calculate in rational microdollars before
// converting to integer microcredits. A GB is 2^30 bytes in S3 billing.
func storageCost(counts usage.Counts, stored usage.Stored, days int, s dropapi.CreditSettings) *big.Rat {
	cost := new(big.Rat)
	add := func(n, rate, den int64) {
		cost.Add(cost, new(big.Rat).Mul(big.NewRat(n, den), big.NewRat(rate, 1)))
	}
	add(counts.ClassA, 5, 1)
	add(counts.ClassB, 4, 10)
	add(counts.BytesOut, 90000, usage.GB)
	add(stored.CurrentBytes, 23000, usage.GB*int64(days))
	add(stored.NoncurrentBytes, 23000, usage.GB*int64(days))
	cost.Mul(cost, big.NewRat(s.MarkupPPM, s.EuroMicrosPerCredit))
	cost.Mul(cost, big.NewRat(s.EURPerUSDPPM, 1000000))
	return cost
}

func chargeS3(tx core.App, id, source string, c usage.Counts, stored usage.Stored, days int) error {
	return chargeS3At(tx, id, source, c, stored, days, nil)
}

func chargeS3At(tx core.App, id, source string, c usage.Counts, stored usage.Stored, days int, pricing *dropapi.CreditSettings) error {
	a, err := tx.FindRecordById("apps", id)
	if err != nil {
		return err
	}
	owner := a.GetString("owner")
	state, err := balanceOf(tx, owner)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("s3:%x", sha256.Sum256([]byte(id+"\x00"+source)))
	receipt, err := tx.DB().NewQuery("INSERT OR IGNORE INTO drop_credit_s3_receipts(source) VALUES({:s})").Bind(dbx.Params{"s": key}).Execute()
	if err != nil {
		return err
	}
	inserted, err := receipt.RowsAffected()
	if err != nil || inserted == 0 {
		return err
	}
	// Retained versions remain visible to inventory even at zero. A
	// late observation cannot cause an overdraft or a charge on replay.
	available := state.Balance - state.Reserved
	if available <= 0 {
		return nil
	}
	settings, err := creditSettings(tx)
	if err != nil {
		return err
	}
	if pricing != nil {
		settings = *pricing
	}
	cost := storageCost(c, stored, days, settings)
	var saved string
	err = tx.DB().NewQuery("SELECT fraction FROM drop_credit_s3_carry WHERE app={:a}").Bind(dbx.Params{"a": id}).Row(&saved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if saved != "" {
		fraction, ok := new(big.Rat).SetString(saved)
		if !ok {
			return errors.New("invalid storage credit carry")
		}
		cost.Add(cost, fraction)
	}
	amount := new(big.Int).Quo(cost.Num(), cost.Denom())
	remainder := new(big.Rat).Sub(cost, new(big.Rat).SetInt(amount))
	if amount.Cmp(big.NewInt(available)) >= 0 {
		amount.SetInt64(available)
		remainder.SetInt64(0)
	}
	_, err = tx.DB().NewQuery("INSERT INTO drop_credit_s3_carry(app,fraction) VALUES({:a},{:f}) ON CONFLICT(app) DO UPDATE SET fraction=excluded.fraction").Bind(dbx.Params{"a": id, "f": remainder.RatString()}).Execute()
	if err != nil || amount.Sign() == 0 {
		return err
	}
	if err := appendCredit(tx, owner, id, "s3", key, -amount.Int64()); err != nil {
		return err
	}
	if pricing != nil {
		// Reconciliation nets the provisional credit and final charge before applying a transition.
		return nil
	}
	state.Balance -= amount.Int64()
	if state.Balance == 0 {
		if err := changeStorageMode(tx, owner, false); err != nil {
			return err
		}
	}
	return enforceCredits(tx, owner, state)
}
func chargeInventory(tx core.App, id, day string, stored usage.Stored) error {
	date, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return err
	}
	days := time.Date(date.Year(), date.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return chargeS3(tx, id, "inventory:"+day, usage.Counts{}, stored, days)
}
