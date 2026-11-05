package analytics

import (
	"context"
	"fmt"
)

// AppsInput is the input of list_apps.
type AppsInput struct {
	Environment string `json:"environment,omitempty" jsonschema:"which events the totals count: production (default: real App Store users only, test builds excluded), testflight, development or all"`
}

// AppInfo is one row of list_apps.
type AppInfo struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	CreatedAt      string  `json:"created_at"`
	Events         int64   `json:"events"`
	Users          int64   `json:"users"`
	FirstEventAt   *string `json:"first_event_at"`
	LastEventAt    *string `json:"last_event_at"`
	LastReceivedAt *string `json:"last_received_at"`
}

// AppsResult is the output of list_apps.
type AppsResult struct {
	Environment string    `json:"environment"`
	Apps        []AppInfo `json:"apps"`
}

// ListApps returns every app (always all of them) with all-time totals over
// the chosen environment.
func (s *Service) ListApps(ctx context.Context, in AppsInput) (AppsResult, error) {
	env, envs, err := resolveEnv(in.Environment)
	if err != nil {
		return AppsResult{}, err
	}
	cond, envArgs := envCond("e.environment", envs)
	from := "FROM events e WHERE e.app_id = a.id AND " + cond
	var args []any
	for range 5 {
		args = append(args, envArgs...)
	}
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(`
		SELECT a.id, a.name, a.created_at,
		       (SELECT count(*) %[1]s),
		       (SELECT count(DISTINCT user_id) %[1]s),
		       (SELECT min(ts) %[1]s),
		       (SELECT max(ts) %[1]s),
		       (SELECT max(received_at) %[1]s)
		FROM apps a ORDER BY a.name`, from), args...)
	if err != nil {
		return AppsResult{}, err
	}
	defer rows.Close()
	res := AppsResult{Environment: env, Apps: []AppInfo{}}
	for rows.Next() {
		var a AppInfo
		var created int64
		var first, last, recv *int64
		if err := rows.Scan(&a.ID, &a.Name, &created, &a.Events, &a.Users, &first, &last, &recv); err != nil {
			return AppsResult{}, err
		}
		a.CreatedAt = FormatTime(created)
		a.FirstEventAt, a.LastEventAt, a.LastReceivedAt = optTime(first), optTime(last), optTime(recv)
		res.Apps = append(res.Apps, a)
	}
	return res, rows.Err()
}

func optTime(ms *int64) *string {
	if ms == nil {
		return nil
	}
	s := FormatTime(*ms)
	return &s
}
