package analytics

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meltinbitfarm/sonda/internal/store"
)

// now is a Wednesday.
var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t    *testing.T
	st   *store.Store
	svc  *Service
	apps map[string]int64
	n    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ro, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	f := &fixture{t: t, st: st, svc: &Service{DB: ro.DB, Now: func() time.Time { return now }}, apps: map[string]int64{}}
	for _, name := range []string{"Alpha", "Beta"} {
		a, err := st.CreateApp(ctx, name, "hash-"+name, now.Add(-100*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		f.apps[name] = a.ID
	}
	return f
}

// uid turns a short label into a stable lowercase UUID.
func uid(label string) string {
	var h uint64 = 1469598103934665603
	for _, c := range label {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", h>>32, h&0xffffffffffff)
}

type ev struct {
	app, name     string
	at            time.Time
	user, session string
	props         string
	version       string
	deviceType    string
	storefront    string
}

func (f *fixture) add(evs ...ev) {
	f.t.Helper()
	for _, e := range evs {
		f.n++
		app := e.app
		if app == "" {
			app = "Alpha"
		}
		sess := e.session
		if sess == "" {
			sess = "s-" + e.user
		}
		se := store.Event{
			EventID: uid(fmt.Sprint("event", f.n)), Name: e.name, TS: e.at.UnixMilli(),
			UserID: uid(e.user), SessionID: uid(sess), AppVersion: e.version,
			DeviceType: e.deviceType, Storefront: e.storefront, Props: e.props,
		}
		if _, err := f.st.InsertEvents(context.Background(), f.apps[app], e.at.Add(time.Minute), []store.Event{se}); err != nil {
			f.t.Fatal(err)
		}
	}
}

func day(d int, h int) time.Time {
	return now.AddDate(0, 0, -d).Truncate(24 * time.Hour).Add(time.Duration(h) * time.Hour)
}

func TestResolveApp(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, in := range []string{"Alpha", "alpha", " ALPHA ", fmt.Sprint(f.apps["Alpha"])} {
		a, err := f.svc.resolveApp(ctx, in)
		if err != nil || a.ID != f.apps["Alpha"] || a.Name != "Alpha" {
			t.Errorf("resolveApp(%q) = %+v, %v", in, a, err)
		}
	}
	for _, in := range []string{"", "Gamma", "999"} {
		_, err := f.svc.resolveApp(ctx, in)
		if err == nil || !strings.Contains(err.Error(), `"Alpha" (id 1)`) || !strings.Contains(err.Error(), `"Beta" (id 2)`) {
			t.Errorf("resolveApp(%q) err = %v", in, err)
		}
	}
}

func TestResolveScope(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sc, err := f.svc.resolveScope(ctx, "Alpha", "", "")
	if err != nil || sc.to != now.UnixMilli() || sc.from != now.Add(-30*24*time.Hour).UnixMilli() {
		t.Fatalf("default scope = %+v, %v", sc, err)
	}
	sc, err = f.svc.resolveScope(ctx, "Alpha", "2026-10-01", "2026-10-31")
	if err != nil || sc.period() != (Period{"2026-10-01T00:00:00.000Z", "2026-11-01T00:00:00.000Z"}) {
		t.Fatalf("date scope = %+v, %v", sc.period(), err)
	}
	sc, err = f.svc.resolveScope(ctx, "Alpha", "2026-10-01T10:00:00+02:00", "2026-10-02T00:00:00Z")
	if err != nil || sc.period().From != "2026-10-01T08:00:00.000Z" || sc.period().To != "2026-10-02T00:00:00.000Z" {
		t.Fatalf("rfc3339 scope = %+v, %v", sc.period(), err)
	}
	for _, c := range [][2]string{{"yesterday", ""}, {"", "11/04/2026"}, {"2026-11-05", "2026-11-01"}} {
		if _, err := f.svc.resolveScope(ctx, "Alpha", c[0], c[1]); err == nil {
			t.Errorf("scope %q..%q: no error", c[0], c[1])
		}
	}
}

func TestPropsCond(t *testing.T) {
	cond, args, err := propsCond(map[string]any{"step": float64(2), "name": "x", "ok": true, "gone": nil, "ratio": 0.5})
	if err != nil {
		t.Fatal(err)
	}
	want := "json_extract(props, ?) IS NULL AND json_extract(props, ?) = ? AND json_extract(props, ?) = ? AND json_extract(props, ?) = ? AND json_extract(props, ?) = ?"
	if cond != want {
		t.Fatalf("cond = %s", cond)
	}
	wantArgs := []any{"$.gone", "$.name", "x", "$.ok", 1, "$.ratio", 0.5, "$.step", int64(2)}
	if fmt.Sprint(args) != fmt.Sprint(wantArgs) {
		t.Fatalf("args = %v", args)
	}
	for _, bad := range []map[string]any{
		{"a') OR 1=1 --": "x"}, {"$.x": "x"}, {"a.b": "x"}, {"A": "x"}, {"_a": "x"}, {"": "x"},
		{strings.Repeat("a", 65): "x"}, {"a": []any{1}}, {"a": map[string]any{}},
	} {
		if _, _, err := propsCond(bad); err == nil {
			t.Errorf("propsCond(%v): no error", bad)
		}
	}
}

func TestListApps(t *testing.T) {
	f := newFixture(t)
	f.add(ev{name: "app.first_launch", at: day(3, 9), user: "u1"},
		ev{name: "screen.viewed", at: day(1, 9), user: "u1"},
		ev{name: "screen.viewed", at: day(1, 10), user: "u2"})
	res, err := f.svc.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Apps) != 2 {
		t.Fatalf("apps = %+v", res.Apps)
	}
	a, b := res.Apps[0], res.Apps[1]
	if a.Name != "Alpha" || a.Events != 3 || a.Users != 2 || *a.FirstEventAt != FormatTime(day(3, 9).UnixMilli()) ||
		*a.LastEventAt != FormatTime(day(1, 10).UnixMilli()) || *a.LastReceivedAt != FormatTime(day(1, 10).Add(time.Minute).UnixMilli()) {
		t.Errorf("alpha = %+v", a)
	}
	if b.Name != "Beta" || b.Events != 0 || b.FirstEventAt != nil || b.LastEventAt != nil {
		t.Errorf("beta = %+v", b)
	}
}

func seedCounts(f *fixture) {
	f.add(
		ev{name: "screen.viewed", at: day(1, 9), user: "u1", props: `{"screen":"home"}`, version: "1.0", deviceType: "phone", storefront: "ITA"},
		ev{name: "screen.viewed", at: day(1, 10), user: "u1", props: `{"screen":"settings"}`, version: "1.0", deviceType: "phone", storefront: "ITA"},
		ev{name: "screen.viewed", at: day(2, 9), user: "u2", props: `{"screen":"home"}`, version: "1.1", deviceType: "pad", storefront: "USA"},
		ev{name: "onboarding.step_viewed", at: day(2, 10), user: "u2", props: `{"step":2,"step_name":"intro"}`, version: "1.1", deviceType: "pad"},
		ev{name: "onboarding.step_viewed", at: day(2, 11), user: "u3", props: `{"step":3,"step_name":"perms"}`, version: "1.1", deviceType: "phone"},
		ev{name: "screen.viewed", at: day(40, 9), user: "u4", props: `{"screen":"home"}`}, // outside the default range
		ev{app: "Beta", name: "screen.viewed", at: day(1, 9), user: "u9", props: `{"screen":"home"}`},
	)
}

func TestEventCounts(t *testing.T) {
	f := newFixture(t)
	seedCounts(f)
	ctx := context.Background()

	res, err := f.svc.EventCounts(ctx, CountsInput{App: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != (Totals{5, 3}) || len(res.Rows) != 1 || res.Rows[0].Events != 5 || res.Rows[0].Users != 3 {
		t.Fatalf("totals = %+v rows %+v", res.Total, res.Rows)
	}

	res, err = f.svc.EventCounts(ctx, CountsInput{App: "Alpha", GroupBy: []string{"name"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(rowsSummary(res)); got != "[name=screen.viewed:3/2 name=onboarding.step_viewed:2/2]" {
		t.Errorf("by name = %s", got)
	}

	res, err = f.svc.EventCounts(ctx, CountsInput{App: "Alpha", Names: []string{"screen.viewed"}, GroupBy: []string{"prop:screen"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(rowsSummary(res)); got != "[prop:screen=home:2/2 prop:screen=settings:1/1]" {
		t.Errorf("by prop = %s", got)
	}

	// Numeric prop filter matches the stored number, the string "2" does not.
	res, _ = f.svc.EventCounts(ctx, CountsInput{App: "Alpha", Props: map[string]any{"step": float64(2)}})
	if res.Total.Events != 1 {
		t.Errorf("step=2 events = %d", res.Total.Events)
	}
	res, _ = f.svc.EventCounts(ctx, CountsInput{App: "Alpha", Props: map[string]any{"step": "2"}})
	if res.Total.Events != 0 {
		t.Errorf(`step="2" events = %d`, res.Total.Events)
	}
	res, _ = f.svc.EventCounts(ctx, CountsInput{App: "Alpha", Props: map[string]any{"step_name": "perms"}})
	if res.Total.Events != 1 || res.Total.Users != 1 {
		t.Errorf("step_name=perms = %+v", res.Total)
	}

	res, err = f.svc.EventCounts(ctx, CountsInput{App: "Alpha", GroupBy: []string{"device_type", "app_version"}, Bucket: "day"})
	if err != nil {
		t.Fatal(err)
	}
	want := "[" + day(2, 0).Format(time.DateOnly) + " app_version=1.1,device_type=pad:2/1 " +
		day(2, 0).Format(time.DateOnly) + " app_version=1.1,device_type=phone:1/1 " +
		day(1, 0).Format(time.DateOnly) + " app_version=1.0,device_type=phone:2/1]"
	if got := fmt.Sprint(rowsSummary(res)); got != want {
		t.Errorf("by day:\n got %s\nwant %s", got, want)
	}

	// Weekly buckets start on Monday: 2026-11-02 for this week.
	res, _ = f.svc.EventCounts(ctx, CountsInput{App: "Alpha", Bucket: "week", From: "2026-09-01"})
	if got := fmt.Sprint(rowsSummary(res)); got != "[2026-09-21 :1/1 2026-11-02 :5/3]" {
		t.Errorf("by week = %s", got)
	}

	// Storefront NULL is its own group.
	res, _ = f.svc.EventCounts(ctx, CountsInput{App: "Alpha", GroupBy: []string{"storefront"}})
	if got := fmt.Sprint(rowsSummary(res)); got != "[storefront=<nil>:2/2 storefront=ITA:2/1 storefront=USA:1/1]" {
		t.Errorf("by storefront = %s", got)
	}

	res, _ = f.svc.EventCounts(ctx, CountsInput{App: "Alpha", GroupBy: []string{"name"}, Limit: 1})
	if len(res.Rows) != 1 || !res.Truncated {
		t.Errorf("limit 1: %d rows truncated=%v", len(res.Rows), res.Truncated)
	}

	res, _ = f.svc.EventCounts(ctx, CountsInput{App: "Beta"})
	if res.Total.Events != 1 {
		t.Errorf("Beta events = %d", res.Total.Events)
	}

	for _, bad := range []CountsInput{
		{App: "Alpha", GroupBy: []string{"user_id"}},
		{App: "Alpha", GroupBy: []string{"name; DROP TABLE events"}},
		{App: "Alpha", GroupBy: []string{"prop:Bad-Key"}},
		{App: "Alpha", GroupBy: []string{"name", "name"}},
		{App: "Alpha", GroupBy: []string{"name", "build", "locale", "region"}},
		{App: "Alpha", Bucket: "month"},
		{App: "Alpha", Props: map[string]any{"x') OR ('1'='1": 1.0}},
		{App: "Alpha", Names: []string{strings.Repeat("x", 65)}},
	} {
		if _, err := f.svc.EventCounts(ctx, bad); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

func rowsSummary(res CountsResult) []string {
	var out []string
	for _, r := range res.Rows {
		var parts []string
		for _, g := range sortedKeys(r.Group) {
			parts = append(parts, fmt.Sprintf("%s=%v", g, r.Group[g]))
		}
		s := strings.Join(parts, ",")
		if r.Bucket != "" {
			s = r.Bucket + " " + s
		}
		out = append(out, fmt.Sprintf("%s:%d/%d", s, r.Events, r.Users))
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	for i := range ks {
		for j := i + 1; j < len(ks); j++ {
			if ks[j] < ks[i] {
				ks[i], ks[j] = ks[j], ks[i]
			}
		}
	}
	return ks
}
