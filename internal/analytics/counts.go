package analytics

import (
	"context"
	"fmt"
	"strings"
)

const (
	defaultCountRows = 500
	maxCountRows     = 5000
	maxGroupBy       = 3
)

// groupColumns maps group_by values to fixed SQL expressions. "prop:<key>"
// is handled separately with a bound JSON path.
var groupColumns = map[string]string{
	"name":         "name",
	"app_version":  "app_version",
	"build":        "build",
	"os_version":   "os_version",
	"storefront":   "storefront",
	"region":       "region",
	"locale":       "locale",
	"device_type":  "device_type",
	"device_model": "device_model",
}

// bucketExprs are UTC days and Monday-starting weeks, as YYYY-MM-DD.
var bucketExprs = map[string]string{
	"day":  "date(ts / 1000, 'unixepoch')",
	"week": "date(ts / 1000, 'unixepoch', 'weekday 0', '-6 days')",
}

// CountsInput is the input of event_counts.
type CountsInput struct {
	App     string         `json:"app" jsonschema:"app name or numeric id (see list_apps)"`
	From    string         `json:"from,omitempty" jsonschema:"start, inclusive: YYYY-MM-DD (UTC) or RFC 3339; default 30 days before 'to'"`
	To      string         `json:"to,omitempty" jsonschema:"end: YYYY-MM-DD (that whole UTC day included) or RFC 3339 (exclusive); default now"`
	Names   []string       `json:"names,omitempty" jsonschema:"only these event names, e.g. [\"paywall.shown\", \"purchase.completed\"]"`
	Props   map[string]any `json:"props,omitempty" jsonschema:"equality filter on props, e.g. {\"placement\": \"onboarding\"}; values string, number, boolean, or null for a missing prop"`
	GroupBy []string       `json:"group_by,omitempty" jsonschema:"up to 3 of: name, app_version, build, os_version, storefront, region, locale, device_type, device_model, prop:<key> (e.g. prop:screen)"`
	Bucket  string         `json:"bucket,omitempty" jsonschema:"optional time bucket: day or week (UTC, weeks start Monday)"`
	Limit   int            `json:"limit,omitempty" jsonschema:"max rows, default 500, max 5000"`
}

// CountRow is one group (and bucket) of event_counts.
type CountRow struct {
	Bucket string         `json:"bucket,omitempty"`
	Group  map[string]any `json:"group,omitempty"`
	Events int64          `json:"events"`
	Users  int64          `json:"users"`
}

// Totals are computed over the whole filtered range, not summed from rows
// (unique users cannot be summed).
type Totals struct {
	Events int64 `json:"events"`
	Users  int64 `json:"users"`
}

// CountsResult is the output of event_counts.
type CountsResult struct {
	App       AppRef     `json:"app"`
	Period    Period     `json:"period"`
	GroupBy   []string   `json:"group_by"`
	Bucket    string     `json:"bucket,omitempty"`
	Total     Totals     `json:"total"`
	Rows      []CountRow `json:"rows"`
	Truncated bool       `json:"truncated"`
}

// EventCounts counts events and unique users, optionally grouped and bucketed.
func (s *Service) EventCounts(ctx context.Context, in CountsInput) (CountsResult, error) {
	sc, err := s.resolveScope(ctx, in.App, in.From, in.To)
	if err != nil {
		return CountsResult{}, err
	}
	w := newWhere(sc)
	if err := w.names(in.Names); err != nil {
		return CountsResult{}, err
	}
	if err := w.props(in.Props); err != nil {
		return CountsResult{}, err
	}
	if len(in.GroupBy) > maxGroupBy {
		return CountsResult{}, fmt.Errorf("at most %d group_by values", maxGroupBy)
	}

	var sel []string
	var selArgs []any
	seen := map[string]bool{}
	for _, g := range in.GroupBy {
		if seen[g] {
			return CountsResult{}, fmt.Errorf("group_by %q repeated", g)
		}
		seen[g] = true
		if key, ok := strings.CutPrefix(g, "prop:"); ok {
			path, err := propPath(key)
			if err != nil {
				return CountsResult{}, err
			}
			sel = append(sel, "json_extract(props, ?)")
			selArgs = append(selArgs, path)
			continue
		}
		col, ok := groupColumns[g]
		if !ok {
			return CountsResult{}, fmt.Errorf("unknown group_by %q; use name, app_version, build, os_version, storefront, region, locale, device_type, device_model or prop:<key>", g)
		}
		sel = append(sel, col)
	}
	if in.Bucket != "" {
		b, ok := bucketExprs[in.Bucket]
		if !ok {
			return CountsResult{}, fmt.Errorf("bucket must be day or week, got %q", in.Bucket)
		}
		sel = append(sel, b)
	}
	limit := clamp(in.Limit, defaultCountRows, maxCountRows)

	res := CountsResult{App: sc.app, Period: sc.period(), GroupBy: in.GroupBy, Bucket: in.Bucket, Rows: []CountRow{}}
	if res.GroupBy == nil {
		res.GroupBy = []string{}
	}
	if err := s.DB.QueryRowContext(ctx,
		"SELECT count(*), count(DISTINCT user_id) FROM events WHERE "+w.sql(), w.args...).
		Scan(&res.Total.Events, &res.Total.Users); err != nil {
		return CountsResult{}, err
	}
	if len(sel) == 0 {
		res.Rows = append(res.Rows, CountRow{Events: res.Total.Events, Users: res.Total.Users})
		return res, nil
	}

	var groupBy []string
	for i := range sel {
		groupBy = append(groupBy, fmt.Sprint(i+1))
	}
	// Buckets ascending, then the biggest groups first; ties by group values
	// (NULL first) so the output is deterministic.
	order := "events DESC"
	groups := groupBy
	if in.Bucket != "" {
		order = fmt.Sprintf("%d, events DESC", len(sel))
		groups = groupBy[:len(groupBy)-1]
	}
	if len(groups) > 0 {
		order += ", " + strings.Join(groups, ", ")
	}
	q := fmt.Sprintf("SELECT %s, count(*) AS events, count(DISTINCT user_id) FROM events WHERE %s GROUP BY %s ORDER BY %s LIMIT ?",
		strings.Join(sel, ", "), w.sql(), strings.Join(groupBy, ", "), order)
	args := append(append(append([]any{}, selArgs...), w.args...), limit+1)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return CountsResult{}, err
	}
	defer rows.Close()
	for rows.Next() {
		vals := make([]any, len(sel)+2)
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return CountsResult{}, err
		}
		if len(res.Rows) == limit {
			res.Truncated = true
			break
		}
		var r CountRow
		if len(in.GroupBy) > 0 {
			r.Group = map[string]any{}
			for i, g := range in.GroupBy {
				r.Group[g] = jsonValue(vals[i])
			}
		}
		if in.Bucket != "" {
			r.Bucket, _ = jsonValue(vals[len(sel)-1]).(string)
		}
		r.Events, _ = vals[len(sel)].(int64)
		r.Users, _ = vals[len(sel)+1].(int64)
		res.Rows = append(res.Rows, r)
	}
	return res, rows.Err()
}
