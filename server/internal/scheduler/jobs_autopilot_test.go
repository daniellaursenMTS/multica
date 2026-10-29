package scheduler

import (
	"context"
	"testing"
	"time"
)

// TestAdvancedNextRunStrictlyAfterPlanTime is the regression guard for
// MUL-3749's boundary case: the post-dispatch next_run_at write-back must
// land on the slot AFTER the one that just fired, even when this app
// instance's local clock lags the DB clock that judged the plan due.
// Anchoring naively on time.Now() alone could recompute the just-fired
// slot; advancedNextRun floors the anchor at plan_time to prevent it.
// Uses the reported scenario: hourly cron in America/New_York, fired slot
// 03:00 EDT (07:00 UTC), next slot 04:00 EDT (08:00 UTC).
func TestAdvancedNextRunStrictlyAfterPlanTime(t *testing.T) {
	const cron = "0 * * * *"
	const tz = "America/New_York"
	planTime := time.Date(2026, 6, 26, 7, 0, 0, 0, time.UTC)
	want := time.Date(2026, 6, 26, 8, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		now  time.Time
	}{
		{"app clock lags the fired slot (skew)", planTime.Add(-90 * time.Second)},
		{"app clock exactly on the fired slot", planTime},
		{"app clock just after the fired slot (normal)", planTime.Add(5 * time.Second)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := advancedNextRun(cron, tz, planTime, tc.now)
			if !ok {
				t.Fatal("expected ok=true for a valid cron/timezone")
			}
			if !got.Equal(want) {
				t.Fatalf("got %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
			if !got.After(planTime) {
				t.Fatalf("next_run_at %s must be strictly after the fired plan_time %s",
					got.Format(time.RFC3339), planTime.Format(time.RFC3339))
			}
		})
	}
}

// TestAdvancedNextRunInvalidInputsSignalFallback verifies the helper
// reports ok=false (so the handler falls back to the last_fired_at-only
// bump) when the cron or timezone cannot be parsed.
func TestAdvancedNextRunInvalidInputsSignalFallback(t *testing.T) {
	planTime := time.Date(2026, 6, 26, 7, 0, 0, 0, time.UTC)
	if _, ok := advancedNextRun("not a cron", "UTC", planTime, planTime); ok {
		t.Fatal("expected ok=false for an invalid cron expression")
	}
	if _, ok := advancedNextRun("0 * * * *", "Mars/Olympus", planTime, planTime); ok {
		t.Fatal("expected ok=false for an invalid timezone")
	}
}

// TestAutopilotPlansForScopeMaxLateness covers a host that sleeps through a
// weekly slot: with the default window the missed occurrence is skipped, with
// a widened window it fires once on wake and is not replayed afterwards.
// Uses the reported scenario: Monday 07:00 Europe/Copenhagen, never fired,
// host awake again at 05:05:21 UTC (5m21s late) and at 09:05 UTC (3h05 late,
// after the switch to CET).
func TestAutopilotPlansForScopeMaxLateness(t *testing.T) {
	const triggerID = "55c806ef-82b8-47b7-8713-092b66712ca0"
	cache := newAutopilotScheduleCache()
	cache.replace(map[string]autopilotTriggerConfig{
		triggerID: {
			TriggerID:      triggerID,
			CronExpression: "0 7 * * 1",
			Timezone:       "Europe/Copenhagen",
			CreatedAt:      time.Date(2026, 9, 11, 11, 51, 7, 0, time.UTC),
		},
	})
	scope := Scope{Kind: ScopeKindAutopilotTrigger, ID: triggerID}

	cases := []struct {
		name        string
		maxLateness time.Duration
		now         time.Time
		want        time.Time // zero = no plan
	}{
		{"default window, on time", DefaultAutopilotScheduleMaxLateness,
			time.Date(2026, 9, 28, 5, 0, 21, 0, time.UTC), time.Date(2026, 9, 28, 5, 0, 0, 0, time.UTC)},
		{"default window, woke 5m21s late", DefaultAutopilotScheduleMaxLateness,
			time.Date(2026, 9, 28, 5, 5, 21, 0, time.UTC), time.Time{}},
		{"12h window, woke 5m21s late", 12 * time.Hour,
			time.Date(2026, 9, 28, 5, 5, 21, 0, time.UTC), time.Date(2026, 9, 28, 5, 0, 0, 0, time.UTC)},
		{"12h window, woke 3h05 late after DST end", 12 * time.Hour,
			time.Date(2026, 10, 26, 9, 5, 0, 0, time.UTC), time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC)},
		{"12h window, woke 13h late", 12 * time.Hour,
			time.Date(2026, 10, 26, 19, 0, 0, 0, time.UTC), time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plans, err := autopilotPlansForScope(cache, tc.maxLateness)(context.Background(), scope, tc.now, LatestPlanInfo{})
			if err != nil {
				t.Fatalf("planner hook: %v", err)
			}
			if tc.want.IsZero() {
				if len(plans) != 0 {
					t.Fatalf("expected no plan, got %v", plans)
				}
				return
			}
			if len(plans) != 1 || !plans[0].Equal(tc.want) {
				t.Fatalf("got %v, want [%s]", plans, tc.want.Format(time.RFC3339))
			}
		})
	}

	// Once the caught-up slot is recorded, the next tick must not replay it.
	fired := time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC)
	plans, err := autopilotPlansForScope(cache, 12*time.Hour)(context.Background(), scope,
		fired.Add(3*time.Hour+30*time.Second), LatestPlanInfo{Found: true, PlanTime: fired, Status: "SUCCESS"})
	if err != nil {
		t.Fatalf("planner hook: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("caught-up slot must not be replayed, got %v", plans)
	}
}
