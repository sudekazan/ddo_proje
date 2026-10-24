// Package audit writes critical operations to the append-only "tn-audit_log" table (eklemeli-yalnız).
package audit

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/sudekazan/ddo_proje/backend/internal/httpx"
)

// Entry describes who did what to which entity, with old and new values.
type Entry struct {
	Actor    int64 // 0 for system actions
	Action   string
	Entity   string
	EntityID int64
	Old      any
	New      any
}

// Write must be called inside the same transaction as the change it records.
func Write(ctx context.Context, tx pgx.Tx, e Entry) error {
	var actor *int64
	if e.Actor != 0 {
		actor = &e.Actor
	}
	oldJSON, err := toJSON(e.Old)
	if err != nil {
		return err
	}
	newJSON, err := toJSON(e.New)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO "tn-audit_log" (actor_user_id, action, entity, entity_id, old_value, new_value, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))`,
		actor, e.Action, e.Entity, strconv.FormatInt(e.EntityID, 10), oldJSON, newJSON, httpx.RequestID(ctx))
	return err
}

func toJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
