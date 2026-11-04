// Package analytics implements the read-only queries behind the MCP tools
// (D14). Every query is parameterized: user input only reaches SQL as bound
// values, and the few identifiers that vary (group-by columns) come from a
// fixed allow-list. Prop keys are validated and used only as bound JSON
// paths in json_extract.
//
// Times: events.ts is the device time in milliseconds since the Unix epoch.
// Ranges are half-open [from, to) on ts; days and weeks are UTC, weeks start
// on Monday.
package analytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultRange is used when "from" is omitted.
	DefaultRange = 30 * 24 * time.Hour
	dayMs        = int64(24 * time.Hour / time.Millisecond)
	maxNameLen   = 64
	maxNames     = 50
	maxProps     = 10
)

var (
	// Same rule as ingestion (step 2): snake_case, at most 64 characters.
	propKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	digitsRe  = regexp.MustCompile(`^[0-9]+$`)
)

// Service runs the queries on a read-only database.
type Service struct {
	DB           *sql.DB
	Now          func() time.Time // clock, for tests; nil means time.Now
	QueryTimeout time.Duration    // query tool timeout; 0 means DefaultQueryTimeout
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// AppRef identifies the app a result refers to.
type AppRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Period is the resolved half-open range [from, to), RFC 3339 UTC.
type Period struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type scope struct {
	app      AppRef
	from, to int64 // ms, [from, to)
}

func (sc scope) period() Period { return Period{FormatTime(sc.from), FormatTime(sc.to)} }

// FormatTime renders milliseconds since the epoch as RFC 3339 UTC with milliseconds.
func FormatTime(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// resolveScope finds the app and parses the range. Defaults: to = now,
// from = to - 30 days.
func (s *Service) resolveScope(ctx context.Context, app, from, to string) (scope, error) {
	a, err := s.resolveApp(ctx, app)
	if err != nil {
		return scope{}, err
	}
	end := s.now()
	if to != "" {
		if end, err = parseTime(to, true); err != nil {
			return scope{}, fmt.Errorf("to: %w", err)
		}
	}
	start := end.Add(-DefaultRange)
	if from != "" {
		if start, err = parseTime(from, false); err != nil {
			return scope{}, fmt.Errorf("from: %w", err)
		}
	}
	if !start.Before(end) {
		return scope{}, errors.New("from must be before to")
	}
	return scope{app: a, from: start.UnixMilli(), to: end.UnixMilli()}, nil
}

// parseTime accepts YYYY-MM-DD (UTC midnight; for an end bound the next
// midnight, so the day is included) or RFC 3339.
func parseTime(v string, end bool) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		if end {
			t = t.Add(24 * time.Hour)
		}
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%q is not YYYY-MM-DD or RFC 3339 (e.g. 2026-11-04 or 2026-11-04T09:30:00Z)", v)
}

// resolveApp accepts a numeric id or a name (exact, then case-insensitive).
func (s *Service) resolveApp(ctx context.Context, app string) (AppRef, error) {
	app = strings.TrimSpace(app)
	if app == "" {
		return AppRef{}, s.unknownApp(ctx, "app is required")
	}
	var a AppRef
	var err error
	if digitsRe.MatchString(app) {
		id, perr := strconv.ParseInt(app, 10, 64)
		if perr == nil {
			err = s.DB.QueryRowContext(ctx, "SELECT id, name FROM apps WHERE id = ?", id).Scan(&a.ID, &a.Name)
			if err == nil {
				return a, nil
			}
		}
	}
	err = s.DB.QueryRowContext(ctx, "SELECT id, name FROM apps WHERE name = ?", app).Scan(&a.ID, &a.Name)
	if errors.Is(err, sql.ErrNoRows) {
		rows, qerr := s.DB.QueryContext(ctx, "SELECT id, name FROM apps WHERE name = ? COLLATE NOCASE LIMIT 2", app)
		if qerr != nil {
			return AppRef{}, qerr
		}
		var found []AppRef
		for rows.Next() {
			var f AppRef
			if err := rows.Scan(&f.ID, &f.Name); err != nil {
				rows.Close()
				return AppRef{}, err
			}
			found = append(found, f)
		}
		rows.Close()
		if len(found) == 1 {
			return found[0], nil
		}
		return AppRef{}, s.unknownApp(ctx, fmt.Sprintf("no app named %q", app))
	}
	return a, err
}

func (s *Service) unknownApp(ctx context.Context, msg string) error {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name FROM apps ORDER BY name")
	if err != nil {
		return errors.New(msg)
	}
	defer rows.Close()
	var list []string
	for rows.Next() {
		var id int64
		var name string
		if rows.Scan(&id, &name) == nil {
			list = append(list, fmt.Sprintf("%q (id %d)", name, id))
		}
	}
	if len(list) == 0 {
		return fmt.Errorf("%s; no apps exist yet", msg)
	}
	return fmt.Errorf("%s; available apps: %s", msg, strings.Join(list, ", "))
}

// propPath validates a prop key and returns its JSON path for json_extract.
func propPath(key string) (string, error) {
	if !propKeyRe.MatchString(key) {
		return "", fmt.Errorf("invalid prop key %q: props keys are snake_case (a-z, 0-9, _), start with a letter, at most 64 characters", key)
	}
	return "$." + key, nil
}

// propsCond returns "json_extract(props, ?) = ? AND ..." for an equality
// filter on scalar props, with its arguments. Keys are sorted so the SQL is
// deterministic. JSON numbers compare numerically, strings as text, booleans
// as 1/0 (how json_extract returns them); null matches a missing or null prop.
func propsCond(props map[string]any) (string, []any, error) {
	if len(props) == 0 {
		return "", nil, nil
	}
	if len(props) > maxProps {
		return "", nil, fmt.Errorf("at most %d props filters", maxProps)
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var conds []string
	var args []any
	for _, k := range keys {
		path, err := propPath(k)
		if err != nil {
			return "", nil, err
		}
		switch v := props[k].(type) {
		case nil:
			conds = append(conds, "json_extract(props, ?) IS NULL")
			args = append(args, path)
			continue
		case string:
			args = append(args, path, v)
		case bool:
			b := 0
			if v {
				b = 1
			}
			args = append(args, path, b)
		case float64:
			if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
				args = append(args, path, int64(v))
			} else {
				args = append(args, path, v)
			}
		default:
			return "", nil, fmt.Errorf("props filter %q: value must be a string, number, boolean or null", k)
		}
		conds = append(conds, "json_extract(props, ?) = ?")
	}
	return strings.Join(conds, " AND "), args, nil
}

func checkNames(names []string) error {
	if len(names) > maxNames {
		return fmt.Errorf("at most %d event names", maxNames)
	}
	for _, n := range names {
		if n == "" || len(n) > maxNameLen {
			return fmt.Errorf("invalid event name %q", n)
		}
	}
	return nil
}

// where builds the common filter: app, range, optional names and props.
type where struct {
	conds []string
	args  []any
}

func newWhere(sc scope) *where {
	return &where{conds: []string{"app_id = ?", "ts >= ?", "ts < ?"}, args: []any{sc.app.ID, sc.from, sc.to}}
}

func (w *where) add(cond string, args ...any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *where) names(names []string) error {
	if err := checkNames(names); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ")
	args := make([]any, len(names))
	for i, n := range names {
		args[i] = n
	}
	w.add("name IN ("+ph+")", args...)
	return nil
}

func (w *where) props(props map[string]any) error {
	cond, args, err := propsCond(props)
	if err != nil || cond == "" {
		return err
	}
	w.add(cond, args...)
	return nil
}

func (w *where) sql() string { return strings.Join(w.conds, " AND ") }

// jsonValue converts a scanned SQLite value to something JSON-friendly.
func jsonValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func clamp(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func ratio(num, den int64) *float64 {
	if den == 0 {
		return nil
	}
	r := math.Round(float64(num)/float64(den)*10000) / 10000
	return &r
}
