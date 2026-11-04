package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/meltinbitfarm/sonda/internal/ingestkey"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sonda.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)

	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != len(ms) {
		t.Fatalf("schema version = %d, %v; want %d", v, err, len(ms))
	}

	// Pragmas are applied on the connection.
	var mode string
	var fk, busy int
	if err := s.DB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
	if err := s.DB.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, %v", fk, err)
	}
	if err := s.DB.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 5000 {
		t.Errorf("busy_timeout = %d, %v", busy, err)
	}

	// Expected indexes exist.
	for _, idx := range []string{"events_app_name_ts", "events_app_user_ts", "events_app_session_ts", "events_app_event_id"} {
		var n int
		s.DB.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", idx).Scan(&n)
		if n != 1 {
			t.Errorf("missing index %s", idx)
		}
	}

	// No IP column anywhere (D9).
	rows, err := s.DB.QueryContext(ctx, "SELECT name FROM pragma_table_info('events')")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		rows.Scan(&c)
		if c == "ip" || c == "ip_address" {
			t.Errorf("events has column %s", c)
		}
	}
	rows.Close()

	// Reopening is idempotent and keeps data.
	if _, err := s.CreateApp(ctx, "a", "h", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	apps, err := s2.ListApps(ctx)
	if err != nil || len(apps) != 1 {
		t.Fatalf("apps after reopen = %v, %v", apps, err)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	if _, err := s.DB.ExecContext(ctx, "PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("expected error opening a newer schema")
	}
}

func TestEventsConstraints(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	app, err := s.CreateApp(ctx, "a", "h", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ins := "INSERT INTO events (app_id, event_id, name, ts, received_at, user_id, session_id, props) VALUES (?, lower(hex(randomblob(16))), 'x', 1, 2, 'u', 's', ?)"
	if _, err := s.DB.ExecContext(ctx, ins, app.ID, `{"k":"v"}`); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	if _, err := s.DB.ExecContext(ctx, ins, app.ID, `not json`); err == nil {
		t.Error("invalid props JSON accepted")
	}
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO events (app_id, name, ts, received_at, user_id, session_id) VALUES (?, 'x', 1, 2, 'u', 's')", app.ID); err == nil {
		t.Error("event without event_id accepted")
	}
	if _, err := s.DB.ExecContext(ctx, ins, app.ID+100, `{}`); err == nil {
		t.Error("event for unknown app accepted (foreign keys off?)")
	}
}

func TestCreateAppAndLookup(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	key, hash := ingestkey.Generate()
	now := time.Date(2026, 11, 4, 12, 0, 0, 123_456_789, time.UTC)
	app, err := s.CreateApp(ctx, "Pulso", hash, now)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.AppByKeyHash(ctx, ingestkey.Hash(key))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != app.ID || got.Name != "Pulso" || !got.CreatedAt.Equal(now.Truncate(time.Millisecond)) {
		t.Fatalf("lookup = %+v, want %+v", got, app)
	}

	if _, err := s.AppByKeyHash(ctx, ingestkey.Hash("sk_wrong")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong key: err = %v", err)
	}
	// The raw key is never stored.
	var n int
	s.DB.QueryRowContext(ctx, "SELECT count(*) FROM apps WHERE ingest_key_hash = ? OR name = ?", key, key).Scan(&n)
	if n != 0 {
		t.Fatal("raw key found in database")
	}

	_, h2 := ingestkey.Generate()
	if _, err := s.CreateApp(ctx, "Pulso", h2, now); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate name: err = %v", err)
	}
}

func TestInsertEvents(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	app, err := s.CreateApp(ctx, "a", "h", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	recv := time.UnixMilli(1_800_000_000_000)
	evs := []Event{
		{EventID: "e1", Name: "app.first_launch", TS: 1, UserID: "u", SessionID: "s", DeviceType: "phone"},
		{EventID: "e2", Name: "screen.viewed", TS: 2, UserID: "u", SessionID: "s", Props: `{"screen":"home"}`},
	}
	if dup, err := s.InsertEvents(ctx, app.ID, recv, evs); err != nil || dup != 0 {
		t.Fatal(dup, err)
	}
	var n int
	var props string
	var build, dt sql.NullString
	var got int64
	s.DB.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE app_id = ? AND received_at = ?", app.ID, recv.UnixMilli()).Scan(&n)
	if n != 2 {
		t.Fatalf("inserted %d rows, want 2", n)
	}
	s.DB.QueryRowContext(ctx, "SELECT props, build, device_type, ts FROM events WHERE name = 'app.first_launch'").Scan(&props, &build, &dt, &got)
	if props != "{}" || build.Valid || dt.String != "phone" || got != 1 {
		t.Fatalf("row = %q %v %v %d", props, build, dt, got)
	}

	// One bad row rolls back the whole batch.
	bad := []Event{{EventID: "e3", Name: "x.y", TS: 3, UserID: "u", SessionID: "s"}, {EventID: "e4", Name: "x.y", TS: 4, UserID: "u", SessionID: "s", Props: "nope"}}
	if _, err := s.InsertEvents(ctx, app.ID, recv, bad); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.InsertEvents(ctx, app.ID, recv, []Event{{Name: "x.y", TS: 3, UserID: "u", SessionID: "s"}}); err == nil {
		t.Fatal("event without event_id accepted")
	}
	s.DB.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&n)
	if n != 2 {
		t.Fatalf("partial insert: %d rows", n)
	}

	// Retried delivery: already stored ids and repeats inside the batch are
	// skipped and counted; the same id in another app is a different event.
	retry := []Event{evs[0], {EventID: "e5", Name: "x.y", TS: 5, UserID: "u", SessionID: "s"}, {EventID: "e5", Name: "x.y", TS: 5, UserID: "u", SessionID: "s"}}
	if dup, err := s.InsertEvents(ctx, app.ID, recv, retry); err != nil || dup != 2 {
		t.Fatalf("retry: dup=%d err=%v", dup, err)
	}
	other, _ := s.CreateApp(ctx, "b", "h2", time.Now())
	if dup, err := s.InsertEvents(ctx, other.ID, recv, evs[:1]); err != nil || dup != 0 {
		t.Fatalf("other app: dup=%d err=%v", dup, err)
	}
	s.DB.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&n)
	if n != 4 {
		t.Fatalf("rows after retry = %d, want 4", n)
	}
}
