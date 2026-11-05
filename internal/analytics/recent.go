package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	defaultRecent = 50
	maxRecent     = 500
)

// RecentInput is the input of recent_events.
type RecentInput struct {
	App         string         `json:"app" jsonschema:"app name or numeric id (see list_apps)"`
	Environment string         `json:"environment,omitempty" jsonschema:"production (default: real App Store users only, test builds excluded), testflight (TestFlight/sandbox builds), development (Debug, simulator, Xcode installs) or all"`
	From        string         `json:"from,omitempty" jsonschema:"start, inclusive: YYYY-MM-DD (UTC) or RFC 3339; default 30 days before 'to'"`
	To          string         `json:"to,omitempty" jsonschema:"end: YYYY-MM-DD (that whole UTC day included) or RFC 3339 (exclusive); default now"`
	Names       []string       `json:"names,omitempty" jsonschema:"only these event names"`
	Props       map[string]any `json:"props,omitempty" jsonschema:"equality filter on props, e.g. {\"screen\": \"settings\"}"`
	UserID      string         `json:"user_id,omitempty" jsonschema:"only this user (random UUID from the SDK)"`
	SessionID   string         `json:"session_id,omitempty" jsonschema:"only this session (UUID)"`
	Limit       int            `json:"limit,omitempty" jsonschema:"max events, default 50, max 500"`
}

// RawEvent is a stored event as returned by recent_events.
type RawEvent struct {
	EventID     string  `json:"event_id"`
	Name        string  `json:"name"`
	TS          string  `json:"ts"`
	ReceivedAt  string  `json:"received_at"`
	UserID      string  `json:"user_id"`
	SessionID   string  `json:"session_id"`
	AppVersion  *string `json:"app_version"`
	Build       *string `json:"build"`
	OSVersion   *string `json:"os_version"`
	Locale      *string `json:"locale"`
	Region      *string `json:"region"`
	Storefront  *string `json:"storefront"`
	DeviceModel *string `json:"device_model"`
	DeviceType  *string `json:"device_type"`
	Environment string  `json:"environment"`
	Props       any     `json:"props"`
}

// RecentResult is the output of recent_events.
type RecentResult struct {
	App         AppRef     `json:"app"`
	Environment string     `json:"environment"`
	Period      Period     `json:"period"`
	Events      []RawEvent `json:"events"`
	Truncated   bool       `json:"truncated"`
}

// RecentEvents returns the newest matching events (by ts), newest first.
func (s *Service) RecentEvents(ctx context.Context, in RecentInput) (RecentResult, error) {
	sc, err := s.resolveScope(ctx, in.App, in.From, in.To, in.Environment)
	if err != nil {
		return RecentResult{}, err
	}
	w := newWhere(sc)
	if err := w.names(in.Names); err != nil {
		return RecentResult{}, err
	}
	if err := w.props(in.Props); err != nil {
		return RecentResult{}, err
	}
	if in.UserID != "" {
		w.add("user_id = ?", in.UserID)
	}
	if in.SessionID != "" {
		w.add("session_id = ?", in.SessionID)
	}
	limit := clamp(in.Limit, defaultRecent, maxRecent)
	rows, err := s.DB.QueryContext(ctx, `SELECT event_id, name, ts, received_at, user_id, session_id,
		app_version, build, os_version, locale, region, storefront, device_model, device_type, environment, props
		FROM events WHERE `+w.sql()+` ORDER BY ts DESC, id DESC LIMIT ?`, append(w.args, limit+1)...)
	if err != nil {
		return RecentResult{}, err
	}
	defer rows.Close()
	res := RecentResult{App: sc.app, Environment: sc.env, Period: sc.period(), Events: []RawEvent{}}
	for rows.Next() {
		var e RawEvent
		var ts, recv int64
		var opt [8]sql.NullString
		var props string
		if err := rows.Scan(&e.EventID, &e.Name, &ts, &recv, &e.UserID, &e.SessionID,
			&opt[0], &opt[1], &opt[2], &opt[3], &opt[4], &opt[5], &opt[6], &opt[7], &e.Environment, &props); err != nil {
			return RecentResult{}, err
		}
		if len(res.Events) == limit {
			res.Truncated = true
			break
		}
		e.TS, e.ReceivedAt = FormatTime(ts), FormatTime(recv)
		dst := []**string{&e.AppVersion, &e.Build, &e.OSVersion, &e.Locale, &e.Region, &e.Storefront, &e.DeviceModel, &e.DeviceType}
		for i, ns := range opt {
			if ns.Valid {
				v := ns.String
				*dst[i] = &v
			}
		}
		d := json.NewDecoder(strings.NewReader(props))
		d.UseNumber()
		if err := d.Decode(&e.Props); err != nil {
			return RecentResult{}, fmt.Errorf("event %s: bad props: %w", e.EventID, err)
		}
		res.Events = append(res.Events, e)
	}
	return res, rows.Err()
}
