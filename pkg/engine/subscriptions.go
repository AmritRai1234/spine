package engine

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// subscriptionsSweep implements the `subscriptions.sweep` action: the recurring
// heart of the subscription commerce tier. It runs on a cron route
//
//	- on: SUBSCRIPTIONS_DUE
//	  cron: 3600s
//	  steps:
//	    - action: subscriptions.sweep
//
// and for every active subscription past its next_run_at it:
//  1. rolls next_run_at forward by the plan interval (30-day months, server clock)
//  2. emits a SUBSCRIPTION_RENEWAL event carrying a snapshot of what to order,
//     so the manifest can build the repeat ORDER through normal routes.
//
// The payload gains `renewed` (count of renewals fired) — surfaced through the
// route's emit state for admin dashboards.
//
// Subscription row contract (columns, schema-evolved by db.insert):
//
//	id            TEXT   subscription id (= originating order_id + plan)
//	status        TEXT   active | paused | cancelled
//	email         TEXT   customer address
//	product_id    TEXT   product to reorder (variants unsupported for renewal)
//	variant_id    TEXT   optional variant to reorder
//	name          TEXT   display snapshot of the product at signup
//	unit_price    REAL   effective per-cycle price (plan discount already applied
//	                     at PLACE_ORDER when the subscription was created)
//	qty           INTEGER units per cycle
//	plan_name     TEXT   selling-plan name (display)
//	interval_months INTEGER cycle length in months
//	next_run_at   TEXT   RFC3339 timestamp of the next due date
//	created_at    TEXT
func (b *Bus) subscriptionsSweep(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	now := time.Now().UTC()
	rows, err := b.db.Query(`SELECT "_spine_id", "id", "status", "email", "product_id", "variant_id",
		"name", "unit_price", "qty", "plan_name", "interval_months", "next_run_at", "created_at"
		FROM "subscriptions" WHERE "status" = 'active'`)
	if err != nil {
		return fmt.Errorf("subscriptions.sweep query failed: %w", err)
	}
	defer rows.Close()

	type sub struct {
		spineID        int64
		id             string
		email          string
		productID      string
		variantID      string
		name           string
		unitPrice      float64
		qty            int
		planName       string
		intervalMonths int
		nextRunAt      time.Time
	}
	var due []sub

	for rows.Next() {
		var s sub
		var status string
		var unitPrice interface{}
		var qtyVal interface{}
		var intervalVal interface{}
		var variant sql.NullString
		var nextRun string
		var createdAt sql.NullString
		if err := rows.Scan(&s.spineID, &s.id, &status, &s.email, &s.productID, &variant,
			&s.name, &unitPrice, &qtyVal, &s.planName, &intervalVal, &nextRun, &createdAt); err != nil {
			log.Printf("[subs] sweep: skipping malformed row: %v", err)
			continue
		}
		s.variantID = variant.String
		s.unitPrice, _ = strconv.ParseFloat(fmt.Sprintf("%v", unitPrice), 64)
		s.qty, _ = strconv.Atoi(fmt.Sprintf("%v", qtyVal))
		s.intervalMonths, _ = strconv.Atoi(fmt.Sprintf("%v", intervalVal))
		if s.intervalMonths < 1 {
			s.intervalMonths = 1
		}
		t, perr := time.Parse(time.RFC3339, strings.TrimSpace(nextRun))
		if perr != nil {
			// tolerate space-separated timestamps
			if t2, err2 := time.Parse("2006-01-02 15:04:05", nextRun); err2 == nil {
				t = t2
			} else {
				log.Printf("[subs] sweep: bad next_run_at %q on %s — resetting to now", nextRun, s.id)
				t = now
			}
		}
		s.nextRunAt = t
		if !t.After(now) {
			due = append(due, s)
		}
	}

	renewed := 0
	for _, s := range due {
		// Roll forward first — even if the renewal event later fails, the
		// subscription must not double-fire on the next tick.
		next := rollMonthly(s.nextRunAt, s.intervalMonths)
		for !next.After(now) { // crash-recovery catch-up without runaway loops
			next = rollMonthly(next, s.intervalMonths)
		}
		if _, err := b.db.Exec(`UPDATE "subscriptions" SET "next_run_at" = ? WHERE "_spine_id" = ?`,
			next.Format(time.RFC3339), s.spineID); err != nil {
			log.Printf("[subs] sweep: rolling %s failed: %v", s.id, err)
			continue
		}

		orderID := generateUUID()
		renPayload := map[string]interface{}{
			"id":              s.id,
			"order_id":        orderID,
			"email":           s.email,
			"cart_id":         "", // renewals bypass carts
			"product_id":      s.productID,
			"variant_id":      s.variantID,
			"name":            s.name,
			"price":           s.unitPrice,
			"qty":             s.qty,
			"plan_name":       s.planName,
			"interval_months": s.intervalMonths,
			"purchase_mode":   "subscription",
			"scheduled_at":    now.Format(time.RFC3339),
		}
		if _, err := b.Emit("SUBSCRIPTION_RENEWAL", renPayload); err != nil {
			log.Printf("[subs] sweep: renewal emit for %s failed: %v", s.id, err)
			continue
		}
		renewed++
	}

	payload["renewed"] = renewed
	if len(due) > 0 {
		log.Printf("[subs] sweep: %d subscription(s) renewed, tick done", renewed)
	}
	return nil
}

// rollMonthly advances t by n calendar-ish months using 30-day months for
// determinism across DST/timezones — same convention as TOGGLE_SUBSCRIPTION's
// next_run_at math in the manifest ($now + months * 2592000).
func rollMonthly(t time.Time, months int) time.Time {
	return t.AddDate(0, months, 0)
}
