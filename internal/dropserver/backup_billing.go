package dropserver

import (
	"context"
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

func backupMonthlyRate(s dropapi.CreditSettings) float64 {
	rate, _ := storageCost(usage.Counts{}, usage.Stored{CurrentBytes: usage.GB}, 1, s).Float64()
	return rate
}

type backupQuote struct {
	MonthlyRate float64 `json:"microcredits_per_gib_month"`
	Spendable   int64   `json:"available_microcredits"`
}

func backupPricing(app core.App, owner string) (backupQuote, error) {
	s, err := creditSettings(app)
	if err != nil {
		return backupQuote{}, err
	}
	state, err := balanceOf(app, owner)
	if err != nil {
		return backupQuote{}, err
	}
	return backupQuote{backupMonthlyRate(s), max(0, state.Balance-state.Reserved)}, nil
}

func requireBackupFunds(app core.App, owner string) error {
	state, err := balanceOf(app, owner)
	if err != nil {
		return err
	}
	if state.Balance <= state.Reserved {
		return Conflict("backup_credits_required", "Add credits to enable or create backups. Existing backups remain available until expiry.")
	}
	return nil
}

type backupMeter struct {
	Owner    string `db:"owner"`
	App      string `db:"app"`
	Bytes    int64  `db:"bytes"`
	Through  int64  `db:"through"`
	End      int64  `db:"end"`
	Markup   int64  `db:"markup"`
	Exchange int64  `db:"exchange"`
	Price    int64  `db:"price"`
}

// Split byte-seconds across UTC month boundaries, using the same GiB-month
// conversion as S3 inventory. Keep the sub-microcredit remainder per wallet.
func backupByteCost(bytes, from, until int64, s dropapi.CreditSettings) *big.Rat {
	cost := new(big.Rat)
	for from < until {
		t := time.Unix(from, 0).UTC()
		boundary := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		end := min(until, boundary.Unix())
		daily := storageCost(usage.Counts{}, usage.Stored{CurrentBytes: bytes}, boundary.AddDate(0, 0, -1).Day(), s)
		cost.Add(cost, daily.Mul(daily, big.NewRat(end-from, 86400)))
		from = end
	}
	return cost
}

func billBackup(tx core.App, id string, at int64) error {
	var b backupMeter
	p := dbx.Params{"b": id, "t": at}
	err := tx.DB().NewQuery(`SELECT b.owner,b.app,b.bytes,m.through,min(b.created+604800,{:t}) AS end,m.markup,m.exchange,m.price
 FROM drop_backups b JOIN drop_backup_meter m ON m.backup=b.id WHERE b.id={:b} AND b.state='ready'`).Bind(p).One(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.Through >= b.End {
		return nil
	}
	state, err := balanceOf(tx, b.Owner)
	if err != nil {
		return err
	}
	if err = waitExec(tx, "UPDATE drop_backup_meter SET through={:t} WHERE backup={:b}", dbx.Params{"t": b.End, "b": id}); err != nil {
		return err
	}
	spendable := state.Balance - state.Reserved
	start := max(b.Through, state.FundedAt)
	if spendable <= 0 || start >= b.End {
		return nil
	}
	s := dropapi.CreditSettings{MarkupPPM: b.Markup, EURPerUSDPPM: b.Exchange, EuroMicrosPerCredit: b.Price}
	cost := backupByteCost(b.Bytes, start, b.End, s)
	var saved string
	err = tx.DB().NewQuery("SELECT fraction FROM drop_backup_carry WHERE owner={:o}").Bind(dbx.Params{"o": b.Owner}).Row(&saved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if saved != "" {
		remainder, ok := new(big.Rat).SetString(saved)
		if !ok {
			return errors.New("invalid backup credit fraction")
		}
		cost.Add(cost, remainder)
	}
	whole := new(big.Int).Quo(cost.Num(), cost.Denom())
	cost.Sub(cost, new(big.Rat).SetInt(whole))
	if whole.Cmp(big.NewInt(spendable)) >= 0 {
		whole.SetInt64(spendable)
		cost.SetInt64(0)
	}
	if err = waitExec(tx, "INSERT INTO drop_backup_carry VALUES({:o},{:f}) ON CONFLICT(owner) DO UPDATE SET fraction=excluded.fraction", dbx.Params{"o": b.Owner, "f": cost.RatString()}); err != nil {
		return err
	}
	if whole.Sign() == 0 {
		return nil
	}
	key := fmt.Sprintf("backup:%s:%d", id, b.End)
	if err = appendCredit(tx, b.Owner, b.App, "backup_storage", key, -whole.Int64()); err != nil {
		return err
	}
	return settleWalletChange(tx, b.Owner, state)
}

// One entry per archive per day, plus its partial final day.
// The ledger, wallet, carry, and metering cursor advance in the same transaction.
func maintainBackupBilling(ctx context.Context, app core.App, at time.Time) error {
	for {
		var ids []string
		err := app.DB().NewQuery(`SELECT m.backup FROM drop_backup_meter m JOIN drop_backups b ON b.id=m.backup
  WHERE b.state='ready' AND m.through<min({:t},b.created+604800) AND
    (m.through<={:t}-86400 OR b.created+604800<={:t}) ORDER BY m.through,m.backup LIMIT 64`).Bind(dbx.Params{"t": at.Unix()}).WithContext(ctx).Column(&ids)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := app.RunInTransaction(func(tx core.App) error { return billBackup(tx, id, at.Unix()) }); err != nil {
				return err
			}
		}
	}
}
