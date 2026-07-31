package engine

import (
	"encoding/json"
	"fmt"
	"time"
)

// ReplayFilter specifies event matching criteria for event replaying.
type ReplayFilter struct {
	EventName string
	FromTime  time.Time
	ToTime    time.Time
	Limit     int
	DryRun    bool
}

// ReplayResult holds the status of an individual replayed event emission.
type ReplayResult struct {
	EventID   int64                  `json:"event_id"`
	EventName string                 `json:"event_name"`
	Payload   map[string]interface{} `json:"payload"`
	Status    string                 `json:"status"`
	Error     string                 `json:"error,omitempty"`
}

// ReplayEvents fetches historical events from _spine_events audit log and re-emits them through current routes.
func (b *Bus) ReplayEvents(filter ReplayFilter) ([]ReplayResult, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	query := `SELECT id, event_name, payload, created_at FROM "_spine_events"`
	var args []interface{}
	var where []string

	if filter.EventName != "" {
		where = append(where, "event_name = ?")
		args = append(args, filter.EventName)
	}
	if !filter.FromTime.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, filter.FromTime.Format(time.RFC3339))
	}
	if !filter.ToTime.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, filter.ToTime.Format(time.RFC3339))
	}

	if len(where) > 0 {
		query += " WHERE "
		for i, w := range where {
			if i > 0 {
				query += " AND "
			}
			query += w
		}
	}
	query += fmt.Sprintf(" ORDER BY id ASC LIMIT %d", limit)

	rows, err := b.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query _spine_events for replay: %w", err)
	}
	defer rows.Close()

	var results []ReplayResult
	for rows.Next() {
		var id int64
		var evtName string
		var rawPayload string
		var createdAt string

		if err := rows.Scan(&id, &evtName, &rawPayload, &createdAt); err != nil {
			continue
		}

		payload := make(map[string]interface{})
		_ = json.Unmarshal([]byte(rawPayload), &payload)

		res := ReplayResult{
			EventID:   id,
			EventName: evtName,
			Payload:   payload,
		}

		if filter.DryRun {
			res.Status = "dry_run"
		} else {
			_, emitErr := b.Emit(evtName, payload)
			if emitErr != nil {
				res.Status = "error"
				res.Error = emitErr.Error()
			} else {
				res.Status = "replayed"
			}
		}

		results = append(results, res)
	}

	return results, nil
}
