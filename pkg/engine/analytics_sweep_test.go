package engine

// Task 5 retention sweep tests: raw events age out on the retention
// window, session rollups are kept, misconfigured retention cannot
// disable the sweep.

import (
	"os"
	"testing"
	"time"
)

func TestSweepPrunesOldEventsKeepsSessions(t *testing.T) {
	eng, _, cleanup := trackEngine(t)
	defer cleanup()
	if err := eng.ensureAnalyticsSchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}

	db := eng.Bus.DB()
	now := time.Now().UnixMilli()
	old := now - 200*24*int64(time.Hour/time.Millisecond) // 200 days ago
	recent := now - 24*int64(time.Hour/time.Millisecond)  // 1 day ago

	seed := `INSERT INTO analytics_events (id, visitor_id, session_id, event_type, page_path, created_at) VALUES (?,?,?,?,?,?)`
	for i, ts := range []int64{old, old, recent} {
		if _, err := db.Exec(seed, genTestID(i), "v", "s", "pageview", "/p", ts); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	// Session rollup that must survive the sweep.
	if _, err := db.Exec(`INSERT INTO analytics_sessions (session_id, visitor_id, started_at, ended_at, pageviews) VALUES ('s','v',?,?,1)`, now, now); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if n := eng.sweepAnalytics(); n != 2 {
		t.Fatalf("sweep deleted %d rows, want 2 (the two 200-day-old events)", n)
	}

	var events, sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM analytics_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM analytics_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("events after sweep: want 1 (recent), got %d", events)
	}
	if sessions != 1 {
		t.Errorf("session rollup must survive sweep, got %d rows", sessions)
	}
}

func TestRetentionDaysEnvHandling(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 180},     // unset → default
		{"30", 30},    // valid override
		{"1", 1},      // aggressive but allowed
		{"0", 180},    // 0 would mean "keep nothing" semantics confusion — rejected
		{"-5", 180},   // negative rejected
		{"garbage", 180}, // garbage rejected
	}
	for _, c := range cases {
		if c.env == "" {
			os.Unsetenv("ANALYTICS_RETENTION_DAYS")
		} else {
			os.Setenv("ANALYTICS_RETENTION_DAYS", c.env)
		}
		if got := analyticsRetentionDays(); got != c.want {
			t.Errorf("retentionDays(%q) = %d, want %d", c.env, got, c.want)
		}
	}
	os.Unsetenv("ANALYTICS_RETENTION_DAYS")
}

func genTestID(i int) string {
	return generateUUID() + "-" + string(rune('a'+i))
}
