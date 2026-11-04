package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func funnelSummary(res FunnelResult) string {
	var parts []string
	for _, s := range res.Steps {
		p := "-"
		if s.ConversionFromPrevious != nil {
			p = fmt.Sprint(*s.ConversionFromPrevious)
		}
		st := "-"
		if s.ConversionFromStart != nil {
			st = fmt.Sprint(*s.ConversionFromStart)
		}
		parts = append(parts, fmt.Sprintf("%d(%s,%s)", s.Count, p, st))
	}
	return strings.Join(parts, " ")
}

func TestFunnel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	onb := `{"placement":"onboarding"}`
	h := time.Hour
	f.add(
		// u1: in order -> 3
		ev{name: "paywall.shown", at: day(10, 9), user: "u1", props: onb},
		ev{name: "purchase.started", at: day(10, 10), user: "u1"},
		ev{name: "purchase.completed", at: day(10, 11), user: "u1"},
		// u2: completed before started -> 2
		ev{name: "paywall.shown", at: day(10, 9), user: "u2", props: onb},
		ev{name: "purchase.completed", at: day(10, 10), user: "u2"},
		ev{name: "purchase.started", at: day(10, 11), user: "u2"},
		// u3: started before the paywall -> 1
		ev{name: "purchase.started", at: day(10, 9), user: "u3"},
		ev{name: "paywall.shown", at: day(10, 10), user: "u3", props: onb},
		// u4: second step 8 days later: outside the default 7-day window
		ev{name: "paywall.shown", at: day(20, 9), user: "u4", props: onb},
		ev{name: "purchase.started", at: day(12, 9), user: "u4"},
		// u5: first attempt never converts, a later one does -> 3
		ev{name: "paywall.shown", at: day(25, 9), user: "u5", props: onb},
		ev{name: "paywall.shown", at: day(5, 9), user: "u5", props: onb},
		ev{name: "purchase.started", at: day(5, 10), user: "u5"},
		ev{name: "purchase.completed", at: day(5, 11), user: "u5"},
		// u6: paywall from another placement -> not in the filtered funnel
		ev{name: "paywall.shown", at: day(10, 9), user: "u6", props: `{"placement":"settings"}`},
		ev{name: "purchase.started", at: day(10, 10), user: "u6"},
		ev{name: "purchase.completed", at: day(10, 11), user: "u6"},
		// u7: first step before the range -> not counted
		ev{name: "paywall.shown", at: day(40, 9), user: "u7", props: onb},
		ev{name: "purchase.started", at: day(9, 9), user: "u7"},
		ev{name: "purchase.completed", at: day(9, 10), user: "u7"},
		// u8: second step after 'to' but inside the window -> 2
		ev{name: "paywall.shown", at: now.Add(-h), user: "u8", props: onb},
		ev{name: "purchase.started", at: now.Add(h), user: "u8"},
		// u9: same millisecond, ordered by arrival (id) -> 3
		ev{name: "paywall.shown", at: day(3, 9), user: "u9", props: onb},
		ev{name: "purchase.started", at: day(3, 9), user: "u9"},
		ev{name: "purchase.completed", at: day(3, 9), user: "u9"},
	)
	steps := []FunnelStep{
		{Name: "paywall.shown", Props: map[string]any{"placement": "onboarding"}},
		{Name: "purchase.started"},
		{Name: "purchase.completed"},
	}
	res, err := f.svc.Funnel(ctx, FunnelInput{App: "Alpha", Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	if got := funnelSummary(res); got != "7(-,1) 5(0.7143,0.7143) 3(0.6,0.4286)" {
		t.Errorf("funnel = %s", got)
	}
	if res.By != "user" || res.WindowHours != 168 || res.Steps[0].Props["placement"] != "onboarding" {
		t.Errorf("result meta = %+v", res)
	}

	// A 10-day window lets u4 reach step 2.
	res, _ = f.svc.Funnel(ctx, FunnelInput{App: "Alpha", Steps: steps, WindowHours: 240})
	if got := funnelSummary(res); got != "7(-,1) 6(0.8571,0.8571) 3(0.5,0.4286)" {
		t.Errorf("funnel 240h = %s", got)
	}
	// A 1-hour window (inclusive): u1 and u5 reach step 2 at exactly +1h but
	// not step 3 at +2h; u2 and u8 have step 2 two hours later; u9 completes.
	res, _ = f.svc.Funnel(ctx, FunnelInput{App: "Alpha", Steps: steps, WindowHours: 1})
	if got := funnelSummary(res); got != "7(-,1) 3(0.4286,0.4286) 1(0.3333,0.1429)" {
		t.Errorf("funnel 1h = %s", got)
	}
	// Without the props filter u6 enters and converts.
	steps[0].Props = nil
	res, _ = f.svc.Funnel(ctx, FunnelInput{App: "Alpha", Steps: steps})
	if got := funnelSummary(res); got != "8(-,1) 6(0.75,0.75) 4(0.6667,0.5)" {
		t.Errorf("funnel no props = %s", got)
	}
	// Explicit range: only the u5 second attempt and u9 fall in the last 6 days.
	res, _ = f.svc.Funnel(ctx, FunnelInput{App: "Alpha", Steps: steps, From: day(6, 0).Format(time.RFC3339), To: day(1, 0).Format(time.DateOnly)})
	if got := funnelSummary(res); got != "2(-,1) 2(1,1) 2(1,1)" {
		t.Errorf("funnel range = %s", got)
	}

	for _, bad := range []FunnelInput{
		{App: "Alpha", Steps: steps[:1]},
		{App: "Alpha", Steps: make([]FunnelStep, 11)},
		{App: "Alpha", Steps: steps, By: "device"},
		{App: "Alpha", Steps: steps, WindowHours: -1},
		{App: "Alpha", Steps: steps, WindowHours: 24*90 + 1},
		{App: "Alpha", Steps: []FunnelStep{{Name: "a.b"}, {Name: ""}}},
		{App: "Alpha", Steps: []FunnelStep{{Name: "a.b", Props: map[string]any{"bad key": 1.0}}, {Name: "c.d"}}},
	} {
		if _, err := f.svc.Funnel(ctx, bad); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

func TestFunnelBySessionAndRepeatedSteps(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.add(
		// u10 sees the paywall in one session and starts a purchase in another.
		ev{app: "Beta", name: "paywall.shown", at: day(3, 9), user: "u10", session: "x1"},
		ev{app: "Beta", name: "purchase.started", at: day(3, 10), user: "u10", session: "x2"},
		ev{app: "Beta", name: "paywall.shown", at: day(3, 9), user: "u11", session: "x3"},
		ev{app: "Beta", name: "purchase.started", at: day(3, 10), user: "u11", session: "x3"},
		ev{app: "Beta", name: "screen.viewed", at: day(2, 9), user: "u12"},
		ev{app: "Beta", name: "screen.viewed", at: day(2, 9), user: "u13"},
		ev{app: "Beta", name: "screen.viewed", at: day(2, 10), user: "u13"},
	)
	steps := []FunnelStep{{Name: "paywall.shown"}, {Name: "purchase.started"}}
	res, _ := f.svc.Funnel(ctx, FunnelInput{App: "Beta", Steps: steps})
	if got := funnelSummary(res); got != "2(-,1) 2(1,1)" {
		t.Errorf("by user = %s", got)
	}
	res, _ = f.svc.Funnel(ctx, FunnelInput{App: "Beta", Steps: steps, By: "session"})
	if got := funnelSummary(res); got != "2(-,1) 1(0.5,0.5)" || res.By != "session" {
		t.Errorf("by session = %s", got)
	}
	// The same event cannot satisfy two steps.
	res, _ = f.svc.Funnel(ctx, FunnelInput{App: "Beta", Steps: []FunnelStep{{Name: "screen.viewed"}, {Name: "screen.viewed"}}})
	if got := funnelSummary(res); got != "2(-,1) 1(0.5,0.5)" {
		t.Errorf("repeated step = %s", got)
	}
	// No data: counts 0, conversions null.
	res, _ = f.svc.Funnel(ctx, FunnelInput{App: "Alpha", Steps: steps})
	if got := funnelSummary(res); got != "0(-,-) 0(-,-)" {
		t.Errorf("empty = %s", got)
	}
}

func TestFunnelDepth(t *testing.T) {
	evs := []funnelEvent{{0, 1}, {5, 2}, {6, 1}, {7, 4}, {9, 2}, {10, 4}}
	for _, c := range []struct {
		window, end int64
		want        int
	}{
		{100, 100, 3}, // 0 -> 5 -> 7
		{5, 100, 3},   // 0 -> 5 -> 7 needs 7: too late; 6 -> 9 -> 10 fits in 4
		{3, 100, 2},   // 6 -> 9; 10 is 4 after 6
		{0, 100, 1},
		{100, 0, 0}, // no start before end
		{100, 6, 3}, // only the start at 0 counts
	} {
		if got := funnelDepth(evs, 3, c.window, c.end); got != c.want {
			t.Errorf("window %d end %d: depth %d, want %d", c.window, c.end, got, c.want)
		}
	}
}

func seedRetention(f *fixture) {
	at := func(s string) time.Time {
		t, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			panic(err)
		}
		return t
	}
	f.add(
		ev{name: "app.first_launch", at: at("2026-09-01 10:00"), user: "r1"},
		ev{name: "app.session_started", at: at("2026-09-02 11:00"), user: "r1"}, // day 1
		ev{name: "app.session_started", at: at("2026-09-03 10:00"), user: "r1"}, // day 2: not measured
		ev{name: "app.session_started", at: at("2026-09-08 12:00"), user: "r1"}, // day 7
		ev{name: "app.first_launch", at: at("2026-09-02 08:00"), user: "r2"},
		ev{name: "screen.viewed", at: at("2026-10-02 13:00"), user: "r2"}, // day 30
		ev{name: "app.first_launch", at: at("2026-09-08 09:00"), user: "r4"},
		ev{name: "app.first_launch", at: at("2026-09-09 09:30"), user: "r4"}, // reinstall: day 1 return, not a new user
		ev{name: "app.first_launch", at: at("2026-11-03 10:00"), user: "r3"}, // too recent to be eligible
		ev{name: "app.session_started", at: at("2026-11-04 11:00"), user: "r3"},
		ev{name: "app.first_launch", at: at("2026-07-01 10:00"), user: "r6"}, // cohort before the range
		ev{name: "app.first_launch", at: at("2026-09-01 10:00"), user: "r6"},
		ev{name: "app.session_started", at: at("2026-09-02 11:00"), user: "r6"},
		ev{app: "Beta", name: "app.first_launch", at: at("2026-09-01 10:00"), user: "r5"},
		ev{app: "Beta", name: "app.session_started", at: at("2026-09-02 10:00"), user: "r5"},
		ev{name: "screen.viewed", at: at("2026-09-05 10:00"), user: "r7"}, // no first_launch: no cohort
	)
}

func retentionSummary(res RetentionResult) string {
	var lines []string
	for _, c := range append(res.Cohorts, res.Overall) {
		var parts []string
		for _, d := range c.Days {
			r := "-"
			if d.Rate != nil {
				r = fmt.Sprint(*d.Rate)
			}
			parts = append(parts, fmt.Sprintf("d%d=%d/%d(%s)", d.Day, d.Returned, d.Eligible, r))
		}
		lines = append(lines, fmt.Sprintf("%s u%d %s", c.WeekStart, c.Users, strings.Join(parts, " ")))
	}
	return strings.Join(lines, "; ")
}

func TestRetention(t *testing.T) {
	f := newFixture(t)
	seedRetention(f)
	ctx := context.Background()

	res, err := f.svc.Retention(ctx, RetentionInput{App: "Alpha", From: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-08-31 u2 d1=1/2(0.5) d7=1/2(0.5) d30=1/2(0.5); " +
		"2026-09-07 u1 d1=1/1(1) d7=0/1(0) d30=0/1(0); " +
		"2026-11-02 u1 d1=0/0(-) d7=0/0(-) d30=0/0(-); " +
		"all u4 d1=2/3(0.6667) d7=1/3(0.3333) d30=1/3(0.3333)"
	if got := retentionSummary(res); got != want {
		t.Errorf("retention:\n got %s\nwant %s", got, want)
	}

	res, _ = f.svc.Retention(ctx, RetentionInput{App: "Alpha", From: "2026-08-01", ReturnEvent: "app.session_started", Days: []int{7, 1}})
	want = "2026-08-31 u2 d1=1/2(0.5) d7=1/2(0.5); 2026-09-07 u1 d1=0/1(0) d7=0/1(0); " +
		"2026-11-02 u1 d1=0/0(-) d7=0/0(-); all u4 d1=1/3(0.3333) d7=1/3(0.3333)"
	if got := retentionSummary(res); got != want || res.ReturnEvent != "app.session_started" {
		t.Errorf("retention session_started:\n got %s\nwant %s", got, want)
	}

	// Default range: last 30 days, only r3's cohort.
	res, _ = f.svc.Retention(ctx, RetentionInput{App: "Alpha"})
	if got := retentionSummary(res); got != "2026-11-02 u1 d1=0/0(-) d7=0/0(-) d30=0/0(-); all u1 d1=0/0(-) d7=0/0(-) d30=0/0(-)" {
		t.Errorf("default range: %s", got)
	}
	// Beta only sees its own user.
	res, _ = f.svc.Retention(ctx, RetentionInput{App: "Beta", From: "2026-08-01", Days: []int{1}})
	if got := retentionSummary(res); got != "2026-08-31 u1 d1=1/1(1); all u1 d1=1/1(1)" {
		t.Errorf("beta: %s", got)
	}

	for _, bad := range []RetentionInput{
		{App: "Alpha", Days: []int{0}},
		{App: "Alpha", Days: []int{366}},
		{App: "Alpha", Days: []int{1, 1}},
		{App: "Alpha", Days: []int{1, 2, 3, 4, 5, 6}},
		{App: "Alpha", ReturnEvent: strings.Repeat("x", 65)},
	} {
		if _, err := f.svc.Retention(ctx, bad); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

func TestRecentEvents(t *testing.T) {
	f := newFixture(t)
	seedCounts(f)
	ctx := context.Background()

	res, err := f.svc.RecentEvents(ctx, RecentInput{App: "Alpha", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 2 || !res.Truncated {
		t.Fatalf("limit 2: %d events truncated=%v", len(res.Events), res.Truncated)
	}
	e := res.Events[0]
	if e.Name != "screen.viewed" || e.TS != FormatTime(day(1, 10).UnixMilli()) || e.UserID != uid("u1") ||
		*e.AppVersion != "1.0" || *e.Storefront != "ITA" || e.Build != nil || e.Locale != nil {
		t.Errorf("newest = %+v", e)
	}
	if res.Events[1].TS != FormatTime(day(1, 9).UnixMilli()) {
		t.Errorf("second = %+v", res.Events[1])
	}

	res, _ = f.svc.RecentEvents(ctx, RecentInput{App: "Alpha", Names: []string{"onboarding.step_viewed"}, UserID: uid("u3")})
	if len(res.Events) != 1 || res.Truncated {
		t.Fatalf("filtered = %+v", res.Events)
	}
	b, _ := json.Marshal(res.Events[0].Props)
	if string(b) != `{"step":3,"step_name":"perms"}` {
		t.Errorf("props = %s", b)
	}

	res, _ = f.svc.RecentEvents(ctx, RecentInput{App: "Alpha", Props: map[string]any{"screen": "home"}, From: "2026-01-01"})
	if len(res.Events) != 3 {
		t.Errorf("screen=home since January = %d events", len(res.Events))
	}
	res, _ = f.svc.RecentEvents(ctx, RecentInput{App: "Alpha", SessionID: uid("s-u2")})
	if len(res.Events) != 2 {
		t.Errorf("session s-u2 = %d events", len(res.Events))
	}
	res, _ = f.svc.RecentEvents(ctx, RecentInput{App: "Alpha", Limit: 10_000})
	if len(res.Events) != 5 || res.Truncated {
		t.Errorf("big limit = %d events", len(res.Events))
	}
}

func TestCheckSelect(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT 1":                       "SELECT 1",
		"  select count(*) from events;": "select count(*) from events",
		"WITH x AS (SELECT 1) SELECT * FROM x ; -- done":  "WITH x AS (SELECT 1) SELECT * FROM x",
		"-- note\n/* c */ SELECT ';' AS semi, \"a;b\"":    "-- note\n/* c */ SELECT ';' AS semi, \"a;b\"",
		"SELECT 'it''s; DELETE FROM events' AS s":         "SELECT 'it''s; DELETE FROM events' AS s",
		"SELECT [delete], `drop` FROM t":                  "SELECT [delete], `drop` FROM t",
		"SELECT * FROM pragma_table_info('events')":       "SELECT * FROM pragma_table_info('events')",
		"SELECT replace(name, '.', '_') FROM events":      "SELECT replace(name, '.', '_') FROM events",
		"SELECT updated_at, deleted FROM x":               "SELECT updated_at, deleted FROM x",
		"SELECT 1e3, 0x1F, x'00'":                         "SELECT 1e3, 0x1F, x'00'",
		"SELECT 1 /* unterminated trailing comment; DROP": "SELECT 1 /* unterminated trailing comment; DROP",
	} {
		got, err := CheckSelect(in)
		if err != nil || got != want {
			t.Errorf("CheckSelect(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{
		"", "   ", "-- only a comment", ";",
		"INSERT INTO apps VALUES (1)",
		"DELETE FROM events",
		"UPDATE events SET name = 'x'",
		"DROP TABLE events",
		"PRAGMA query_only = 0",
		"pragma user_version",
		"ATTACH DATABASE '/tmp/x.db' AS x",
		"DETACH x",
		"VACUUM",
		"BEGIN",
		"CREATE TABLE t (x)",
		"REPLACE INTO apps VALUES (1)",
		"EXPLAIN SELECT 1",
		"VALUES (1)",
		"SELECT 1; SELECT 2",
		"SELECT 1; DELETE FROM events",
		"SELECT 1;; ",
		"WITH x AS (SELECT 1) DELETE FROM events",
		"WITH x AS (SELECT 1) INSERT INTO apps SELECT * FROM x",
		"SELECT 1 FROM x; ATTACH 'y' AS y",
		"SELECT 'unterminated",
		"SELECT \"unterminated",
		"SELECT [unterminated",
		"/* SELECT */ DELETE FROM events",
		"(SELECT 1)",
		strings.Repeat("SELECT 1 UNION ALL ", 2000) + "SELECT 1",
	} {
		if got, err := CheckSelect(in); err == nil {
			t.Errorf("CheckSelect(%q) accepted: %q", in, got)
		}
	}
}

func TestQuery(t *testing.T) {
	f := newFixture(t)
	seedCounts(f)
	ctx := context.Background()

	res, err := f.svc.Query(ctx, QueryInput{SQL: "SELECT name, count(*) AS n, json_extract(props, '$.screen') FROM events WHERE app_id = 1 AND name = 'screen.viewed' GROUP BY 1, 3 ORDER BY 3"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if string(b) != `{"columns":["name","n","json_extract(props, '$.screen')"],"rows":[["screen.viewed",3,"home"],["screen.viewed",1,"settings"]],"row_count":2,"truncated":false}` {
		t.Errorf("query = %s", b)
	}

	res, err = f.svc.Query(ctx, QueryInput{SQL: "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 5000) SELECT x FROM c", MaxRows: 10})
	if err != nil || res.RowCount != 10 || !res.Truncated || res.Rows[9][0] != int64(10) {
		t.Errorf("row limit: %+v, %v", res, err)
	}
	res, _ = f.svc.Query(ctx, QueryInput{SQL: "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 5000) SELECT x FROM c", MaxRows: 5000})
	if res.RowCount != maxQueryRows || !res.Truncated {
		t.Errorf("max rows cap: %d", res.RowCount)
	}

	res, err = f.svc.Query(ctx, QueryInput{SQL: "SELECT x'00ff', x'6869', NULL, 1.5, printf('%.5000c', 'a')"})
	if err != nil {
		t.Fatal(err)
	}
	row := res.Rows[0]
	if row[0] != "base64:AP8=" || row[1] != "hi" || row[2] != nil || row[3] != 1.5 || !strings.HasSuffix(row[4].(string), "…[truncated, 5000 bytes]") {
		t.Errorf("cells = %v", row)
	}

	// Schema introspection through table-valued pragma functions works.
	res, err = f.svc.Query(ctx, QueryInput{SQL: "SELECT name FROM pragma_table_info('events') WHERE name = 'props'"})
	if err != nil || res.RowCount != 1 {
		t.Errorf("pragma_table_info: %+v, %v", res, err)
	}

	// Strings and blobs are capped (O6 M2).
	if _, err := f.svc.Query(ctx, QueryInput{SQL: "SELECT length(hex(zeroblob(100000000)))"}); err == nil || !strings.Contains(err.Error(), "exceeds 16 MiB") {
		t.Errorf("big blob: %v", err)
	}

	// Timeout interrupts a runaway query.
	f.svc.QueryTimeout = 200 * time.Millisecond
	start := time.Now()
	_, err = f.svc.Query(ctx, QueryInput{SQL: "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c"})
	if err == nil || !strings.Contains(err.Error(), "interrupted after 200ms") || time.Since(start) > 5*time.Second {
		t.Errorf("timeout: %v after %s", err, time.Since(start))
	}
}

// Statements that get past CheckSelect still cannot write: the read-only
// connection refuses them (here CheckSelect is bypassed on purpose).
func TestQueryConnectionIsReadOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, stmt := range []string{
		"DELETE FROM events",
		"WITH x AS (SELECT 1) DELETE FROM apps",
		"PRAGMA query_only = 0",
		"ATTACH DATABASE 'file::memory:' AS m",
		"CREATE TEMP TABLE t AS SELECT 1",
	} {
		if _, err := f.svc.DB.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s: executed on the read-only connection", stmt)
		}
	}
	for _, stmt := range []string{"INSERT INTO apps (name) VALUES ('x')", "ATTACH 'x.db' AS x", "PRAGMA writable_schema = 1"} {
		if _, err := f.svc.Query(ctx, QueryInput{SQL: stmt}); err == nil {
			t.Errorf("query tool accepted %s", stmt)
		}
	}
	var n int
	f.st.DB.QueryRowContext(ctx, "SELECT count(*) FROM apps").Scan(&n)
	if n != 2 {
		t.Errorf("apps = %d", n)
	}
}
