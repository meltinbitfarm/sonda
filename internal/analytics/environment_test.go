package analytics

import (
	"context"
	"strings"
	"testing"
)

func TestResolveEnv(t *testing.T) {
	for in, want := range map[string]string{
		"": "production", "production": "production", " TestFlight ": "testflight",
		"development": "development", "ALL": "all",
	} {
		got, envs, err := resolveEnv(in)
		if err != nil || got != want || len(envs) == 0 {
			t.Errorf("resolveEnv(%q) = %q %v %v", in, got, envs, err)
		}
	}
	if _, envs, _ := resolveEnv("all"); len(envs) != 3 {
		t.Errorf("all = %v", envs)
	}
	for _, bad := range []string{"prod", "debug", "staging", "production,testflight", "'; DROP TABLE events; --"} {
		if _, _, err := resolveEnv(bad); err == nil || !strings.Contains(err.Error(), "environment must be") {
			t.Errorf("resolveEnv(%q) err = %v", bad, err)
		}
	}
	if c, args := envCond("e.environment", environments); c != "e.environment IN (?, ?, ?)" || len(args) != 3 {
		t.Errorf("envCond = %q %v", c, args)
	}
}

// seedEnvs: u1 is a real user, u2 a TestFlight tester, u3 Fabio's Debug
// build. u1 also has one development event (same install moved to a Debug
// build): it must not count as a production return.
func seedEnvs(f *fixture) {
	f.add(
		ev{name: "app.first_launch", at: day(20, 9), user: "u1"},
		ev{name: "paywall.shown", at: day(20, 10), user: "u1"},
		ev{name: "purchase.completed", at: day(20, 11), user: "u1"},
		ev{name: "screen.viewed", at: day(13, 9), user: "u1", env: "development"},

		ev{name: "app.first_launch", at: day(20, 9), user: "u2", env: "testflight"},
		ev{name: "paywall.shown", at: day(20, 10), user: "u2", env: "testflight"},
		ev{name: "screen.viewed", at: day(13, 9), user: "u2", env: "testflight"},

		ev{name: "app.first_launch", at: day(20, 9), user: "u3", env: "development"},
		ev{name: "paywall.shown", at: day(20, 10), user: "u3", env: "development"},
		ev{name: "purchase.completed", at: day(20, 11), user: "u3", env: "development"},
	)
}

func TestEnvironmentFilter(t *testing.T) {
	f := newFixture(t)
	seedEnvs(f)
	ctx := context.Background()

	// event_counts: production by default, each environment, all, group_by environment.
	for env, want := range map[string][2]int64{"": {3, 1}, "production": {3, 1}, "testflight": {3, 1}, "development": {4, 2}, "all": {10, 3}} {
		res, err := f.svc.EventCounts(ctx, CountsInput{App: "Alpha", Environment: env})
		if err != nil || res.Total.Events != want[0] || res.Total.Users != want[1] {
			t.Errorf("event_counts env %q = %+v, %v; want %v", env, res.Total, err, want)
		}
		reported := env
		if reported == "" {
			reported = "production"
		}
		if err == nil && res.Environment != reported {
			t.Errorf("event_counts env %q reported %q", env, res.Environment)
		}
	}
	res, err := f.svc.EventCounts(ctx, CountsInput{App: "Alpha", Environment: "all", GroupBy: []string{"environment"}})
	if err != nil || len(res.Rows) != 3 || res.Rows[0].Group["environment"] != "development" || res.Rows[0].Events != 4 ||
		res.Rows[1].Group["environment"] != "production" || res.Rows[2].Group["environment"] != "testflight" {
		t.Errorf("group_by environment = %+v, %v", res.Rows, err)
	}

	// funnel: only u1 converts in production; u3 in development.
	steps := []FunnelStep{{Name: "paywall.shown"}, {Name: "purchase.completed"}}
	for env, want := range map[string][2]int64{"": {1, 1}, "testflight": {1, 0}, "development": {1, 1}, "all": {3, 2}} {
		fr, err := f.svc.Funnel(ctx, FunnelInput{App: "Alpha", Environment: env, Steps: steps})
		if err != nil || fr.Steps[0].Count != want[0] || fr.Steps[1].Count != want[1] {
			t.Errorf("funnel env %q = %+v, %v; want %v", env, fr.Steps, err, want)
		}
	}

	// retention day 7: u1's return is a development event, so not a production return.
	for env, want := range map[string][2]int64{"": {1, 0}, "testflight": {1, 1}, "development": {1, 0}, "all": {3, 2}} {
		rr, err := f.svc.Retention(ctx, RetentionInput{App: "Alpha", Environment: env, Days: []int{7}})
		if err != nil || rr.Overall.Users != want[0] || rr.Overall.Days[0].Returned != want[1] {
			t.Errorf("retention env %q = %+v, %v; want %v", env, rr.Overall, err, want)
		}
	}

	// recent_events: environment on every event.
	re, err := f.svc.RecentEvents(ctx, RecentInput{App: "Alpha", Environment: "testflight"})
	if err != nil || len(re.Events) != 3 || re.Environment != "testflight" {
		t.Fatalf("recent testflight = %+v, %v", re, err)
	}
	for _, e := range re.Events {
		if e.Environment != "testflight" {
			t.Errorf("recent event %s environment %q", e.Name, e.Environment)
		}
	}
	if re, err := f.svc.RecentEvents(ctx, RecentInput{App: "Alpha"}); err != nil || len(re.Events) != 3 || re.Events[0].Environment != "production" {
		t.Errorf("recent default = %+v, %v", re, err)
	}

	// list_apps: totals per environment.
	for env, want := range map[string][2]int64{"": {3, 1}, "development": {4, 2}, "all": {10, 3}} {
		ar, err := f.svc.ListApps(ctx, AppsInput{Environment: env})
		if err != nil || ar.Apps[0].Events != want[0] || ar.Apps[0].Users != want[1] || len(ar.Apps) != 2 {
			t.Errorf("list_apps env %q = %+v, %v; want %v", env, ar, err, want)
		}
	}

	// A bad value is an error everywhere, before any query.
	for name, call := range map[string]func() error{
		"event_counts": func() error {
			_, err := f.svc.EventCounts(ctx, CountsInput{App: "Alpha", Environment: "debug"})
			return err
		},
		"funnel": func() error {
			_, err := f.svc.Funnel(ctx, FunnelInput{App: "Alpha", Environment: "debug", Steps: steps})
			return err
		},
		"retention": func() error {
			_, err := f.svc.Retention(ctx, RetentionInput{App: "Alpha", Environment: "debug"})
			return err
		},
		"recent_events": func() error {
			_, err := f.svc.RecentEvents(ctx, RecentInput{App: "Alpha", Environment: "debug"})
			return err
		},
		"list_apps": func() error { _, err := f.svc.ListApps(ctx, AppsInput{Environment: "debug"}); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "environment must be") {
			t.Errorf("%s with bad environment: %v", name, err)
		}
	}
}
