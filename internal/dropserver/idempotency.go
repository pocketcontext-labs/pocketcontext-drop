package dropserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/dropapi"
)

// opKeyLifetime is how long an idempotency key replays its first result.
const opKeyLifetime = 24 * time.Hour

// opKey is one idempotency key of one account: hashes only, so neither the
// key nor the input is stored.
type opKey struct {
	keyHash, op, inputHash string
}

func newOpKey(user, key, op string, canonicalInput []byte) *opKey {
	k := sha256.Sum256([]byte("drop-op-key-v1\x00" + user + "\x00" + key))
	in := sha256.Sum256(append([]byte("drop-op-input-v1\x00"+op+"\x00"), canonicalInput...))
	return &opKey{keyHash: hex.EncodeToString(k[:]), op: op, inputHash: hex.EncodeToString(in[:])}
}

// lookup returns the stored result of a live key. A key used with another
// operation or input is a conflict; a key whose result was shown only once
// cannot replay it. An expired key is forgotten.
func (k *opKey) lookup(app core.App) (json.RawMessage, bool, error) {
	record, err := app.FindFirstRecordByData("op_keys", "key_hash", k.keyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if record.GetDateTime("created").Time().Before(time.Now().Add(-opKeyLifetime)) {
		return nil, false, app.Delete(record)
	}
	if record.GetString("op") != k.op || record.GetString("input_hash") != k.inputHash {
		return nil, false, Conflict(dropapi.CodeIdempotencyConflict, "This idempotency key was used for a different request. Use a new key.")
	}
	if !record.GetBool("stored") {
		return nil, false, Conflict(dropapi.CodeIdempotencyNoResult, "This request was already applied; its result is shown only once.")
	}
	return json.RawMessage(record.GetString("result")), true, nil
}

// store records the key with the result, or without it when the result is
// shown only once.
func (k *opKey) store(app core.App, result json.RawMessage, keepResult bool) error {
	collection, err := app.FindCachedCollectionByNameOrId("op_keys")
	if err != nil {
		return err
	}
	record := core.NewRecord(collection)
	record.Set("key_hash", k.keyHash)
	record.Set("op", k.op)
	record.Set("input_hash", k.inputHash)
	record.Set("stored", keepResult)
	if keepResult {
		record.Set("result", result)
	} else {
		record.Set("result", json.RawMessage("null"))
	}
	return app.Save(record)
}

// pruneOpKeys deletes expired idempotency keys.
func pruneOpKeys(app core.App, now time.Time) error {
	cutoff, err := types.ParseDateTime(now.Add(-opKeyLifetime))
	if err != nil {
		return err
	}
	_, err = app.DB().NewQuery("DELETE FROM op_keys WHERE created < {:cutoff}").Bind(dbx.Params{"cutoff": cutoff.String()}).Execute()
	return err
}

// runOpKeyPruning prunes at start and hourly until ctx ends.
func runOpKeyPruning(ctx context.Context, app core.App) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := pruneOpKeys(app, time.Now()); err != nil {
			app.Logger().Error("Idempotency key pruning failed", "collection", "op_keys")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
