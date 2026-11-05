package analytics

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// FirstLaunchEvent marks a new user (D12); retention cohorts start from it.
const FirstLaunchEvent = "app.first_launch"

const maxRetentionDays = 5

// DefaultRetentionDays are the return days of D14.
var DefaultRetentionDays = []int{1, 7, 30}

// RetentionInput is the input of retention.
type RetentionInput struct {
	App         string `json:"app" jsonschema:"app name or numeric id (see list_apps)"`
	Environment string `json:"environment,omitempty" jsonschema:"production (default: real App Store users only, test builds excluded), testflight (TestFlight/sandbox builds), development (Debug, simulator, Xcode installs) or all"`
	From        string `json:"from,omitempty" jsonschema:"cohort range start: users whose first app.first_launch is in [from, to). YYYY-MM-DD (UTC) or RFC 3339; default 30 days before 'to'. Day-30 retention needs cohorts older than 31 days, so widen it (e.g. 90 days back)"`
	To          string `json:"to,omitempty" jsonschema:"cohort range end: YYYY-MM-DD (that whole UTC day included) or RFC 3339 (exclusive); default now"`
	Days        []int  `json:"days,omitempty" jsonschema:"return days to measure, default [1, 7, 30]; up to 5 values between 1 and 365"`
	ReturnEvent string `json:"return_event,omitempty" jsonschema:"count a return only for this event name (e.g. app.session_started); default any event"`
}

// RetentionDay is the return on day N for one cohort (or overall).
type RetentionDay struct {
	Day      int      `json:"day"`
	Eligible int64    `json:"eligible"`
	Returned int64    `json:"returned"`
	Rate     *float64 `json:"rate"`
}

// RetentionCohort is one weekly cohort.
type RetentionCohort struct {
	WeekStart string         `json:"week_start"`
	Users     int64          `json:"users"`
	Days      []RetentionDay `json:"days"`
}

// RetentionResult is the output of retention.
type RetentionResult struct {
	App         AppRef            `json:"app"`
	Environment string            `json:"environment"`
	Period      Period            `json:"period"`
	ReturnEvent string            `json:"return_event,omitempty"`
	Cohorts     []RetentionCohort `json:"cohorts"`
	Overall     RetentionCohort   `json:"overall"`
}

// Retention groups users by the UTC week (Monday) of their first
// app.first_launch and measures, for each day N, how many came back in the
// 24 hours starting N*24h after that first launch. Only users whose day-N
// window has fully elapsed are eligible; rate = returned / eligible.
func (s *Service) Retention(ctx context.Context, in RetentionInput) (RetentionResult, error) {
	sc, err := s.resolveScope(ctx, in.App, in.From, in.To, in.Environment)
	if err != nil {
		return RetentionResult{}, err
	}
	days := in.Days
	if len(days) == 0 {
		days = DefaultRetentionDays
	}
	if len(days) > maxRetentionDays {
		return RetentionResult{}, fmt.Errorf("at most %d days", maxRetentionDays)
	}
	days = append([]int{}, days...)
	sort.Ints(days)
	for i, d := range days {
		if d < 1 || d > 365 {
			return RetentionResult{}, fmt.Errorf("days must be between 1 and 365, got %d", d)
		}
		if i > 0 && days[i-1] == d {
			return RetentionResult{}, fmt.Errorf("day %d repeated", d)
		}
	}
	if len(in.ReturnEvent) > maxNameLen {
		return RetentionResult{}, fmt.Errorf("invalid return_event %q", in.ReturnEvent)
	}
	now := s.now().UnixMilli()
	// Cohorts and returns both come from the chosen environments only.
	envSQL, envArgs := envCond("environment", sc.envs)
	eEnvSQL, _ := envCond("e.environment", sc.envs)

	var sel []string
	var selArgs []any
	for _, d := range days {
		start, end := int64(d)*dayMs, int64(d+1)*dayMs
		// Eligible: the day-N window ended before now.
		sel = append(sel, "sum(c.t0 + ? <= ?)")
		selArgs = append(selArgs, end, now)
		ret := "sum(c.t0 + ? <= ? AND EXISTS (SELECT 1 FROM events e WHERE e.app_id = ? AND " + eEnvSQL +
			" AND e.user_id = c.user_id AND e.ts >= c.t0 + ? AND e.ts < c.t0 + ?"
		retArgs := append(append([]any{end, now, sc.app.ID}, envArgs...), start, end)
		if in.ReturnEvent != "" {
			ret += " AND e.name = ?"
			retArgs = append(retArgs, in.ReturnEvent)
		}
		sel = append(sel, ret+"))")
		selArgs = append(selArgs, retArgs...)
	}
	q := fmt.Sprintf(`WITH f AS (
		SELECT user_id, min(ts) AS t0 FROM events WHERE app_id = ? AND %s AND name = ? GROUP BY user_id
	), c AS (SELECT user_id, t0 FROM f WHERE t0 >= ? AND t0 < ?)
	SELECT date(c.t0 / 1000, 'unixepoch', 'weekday 0', '-6 days') AS week, count(*), %s
	FROM c GROUP BY week ORDER BY week`, envSQL, strings.Join(sel, ", "))
	args := append([]any{sc.app.ID}, envArgs...)
	args = append(args, FirstLaunchEvent, sc.from, sc.to)
	args = append(args, selArgs...)

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return RetentionResult{}, err
	}
	defer rows.Close()
	res := RetentionResult{App: sc.app, Environment: sc.env, Period: sc.period(), ReturnEvent: in.ReturnEvent, Cohorts: []RetentionCohort{}}
	res.Overall = RetentionCohort{WeekStart: "all", Days: make([]RetentionDay, len(days))}
	for i, d := range days {
		res.Overall.Days[i].Day = d
	}
	for rows.Next() {
		var c RetentionCohort
		vals := make([]int64, 2*len(days))
		ptrs := []any{&c.WeekStart, &c.Users}
		for i := range vals {
			ptrs = append(ptrs, &vals[i])
		}
		if err := rows.Scan(ptrs...); err != nil {
			return RetentionResult{}, err
		}
		res.Overall.Users += c.Users
		for i, d := range days {
			rd := RetentionDay{Day: d, Eligible: vals[2*i], Returned: vals[2*i+1]}
			rd.Rate = ratio(rd.Returned, rd.Eligible)
			c.Days = append(c.Days, rd)
			res.Overall.Days[i].Eligible += rd.Eligible
			res.Overall.Days[i].Returned += rd.Returned
		}
		res.Cohorts = append(res.Cohorts, c)
	}
	for i := range res.Overall.Days {
		res.Overall.Days[i].Rate = ratio(res.Overall.Days[i].Returned, res.Overall.Days[i].Eligible)
	}
	return res, rows.Err()
}
