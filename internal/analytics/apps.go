package analytics

import "context"

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
	Apps []AppInfo `json:"apps"`
}

// ListApps returns every app with totals over all time.
func (s *Service) ListApps(ctx context.Context) (AppsResult, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT a.id, a.name, a.created_at,
		       (SELECT count(*) FROM events e WHERE e.app_id = a.id),
		       (SELECT count(DISTINCT user_id) FROM events e WHERE e.app_id = a.id),
		       (SELECT min(ts) FROM events e WHERE e.app_id = a.id),
		       (SELECT max(ts) FROM events e WHERE e.app_id = a.id),
		       (SELECT max(received_at) FROM events e WHERE e.app_id = a.id)
		FROM apps a ORDER BY a.name`)
	if err != nil {
		return AppsResult{}, err
	}
	defer rows.Close()
	res := AppsResult{Apps: []AppInfo{}}
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
