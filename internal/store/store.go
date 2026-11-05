// Package store owns the SQLite database: connection settings, embedded
// migrations and queries (D3).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a lookup matches nothing.
var ErrNotFound = errors.New("not found")

// ErrDuplicate is returned when a unique constraint is violated.
var ErrDuplicate = errors.New("already exists")

// MaxWriterConns bounds the read-write pool opened by Open.
const MaxWriterConns = 8

// Store wraps the database handle.
type Store struct {
	DB *sql.DB
}

// App is a row of the apps table.
type App struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

// Open opens (creating if needed) the database at path, applies the
// connection pragmas on every pooled connection and runs pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",   // required by Litestream (D4), readers don't block the writer
		"busy_timeout(5000)",  // wait instead of failing with SQLITE_BUSY
		"foreign_keys(1)",     // enforce events.app_id
		"synchronous(NORMAL)", // safe with WAL, fewer fsyncs
	} {
		q.Add("_pragma", p)
	}
	// BEGIN IMMEDIATE takes the write lock up front, so busy_timeout applies
	// instead of a deadlock error when a read transaction later tries to write.
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// Bounded pool (O6 M1): every pooled connection is a full SQLite handle
	// with its own page cache, so concurrency must not decide how many exist.
	// Writes serialise on SQLite's lock anyway; WAL lets the reads go on.
	db.SetMaxOpenConns(MaxWriterConns)
	db.SetMaxIdleConns(MaxWriterConns)
	s := &Store{DB: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("bad migration file name %q", e.Name())
		}
		b, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{v, e.Name(), string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: expected version %d", m.name, i+1)
		}
	}
	return ms, nil
}

// SchemaVersion returns PRAGMA user_version, the last applied migration.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.DB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// migrate applies, each in its own transaction, the migrations whose number
// is above PRAGMA user_version, then bumps user_version in the same
// transaction. A database newer than the binary is refused.
func (s *Store) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	cur, err := s.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if cur > len(ms) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", cur, len(ms))
	}
	for _, m := range ms[cur:] {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

// CreateApp inserts an app with the given ingest key hash.
func (s *Store) CreateApp(ctx context.Context, name, keyHash string, now time.Time) (App, error) {
	res, err := s.DB.ExecContext(ctx,
		"INSERT INTO apps (name, ingest_key_hash, created_at) VALUES (?, ?, ?)",
		name, keyHash, now.UnixMilli())
	if err != nil {
		var se *sqlite.Error
		if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return App{}, ErrDuplicate
		}
		return App{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return App{}, err
	}
	return App{ID: id, Name: name, CreatedAt: time.UnixMilli(now.UnixMilli()).UTC()}, nil
}

// AppByKeyHash returns the app whose ingest key hashes to keyHash.
func (s *Store) AppByKeyHash(ctx context.Context, keyHash string) (App, error) {
	var a App
	var created int64
	err := s.DB.QueryRowContext(ctx,
		"SELECT id, name, created_at FROM apps WHERE ingest_key_hash = ?", keyHash).
		Scan(&a.ID, &a.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	if err != nil {
		return App{}, err
	}
	a.CreatedAt = time.UnixMilli(created).UTC()
	return a, nil
}

// RotateKey replaces the ingest key hash of the app named name (exact match).
// The old key stops working at once: ingestion looks the hash up on every
// request and nothing caches it. ErrNotFound if no app has that name.
func (s *Store) RotateKey(ctx context.Context, name, keyHash string) (App, error) {
	var a App
	var created int64
	err := s.DB.QueryRowContext(ctx,
		"UPDATE apps SET ingest_key_hash = ? WHERE name = ? RETURNING id, name, created_at",
		keyHash, name).Scan(&a.ID, &a.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return App{}, ErrNotFound
	}
	if err != nil {
		var se *sqlite.Error
		if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return App{}, ErrDuplicate
		}
		return App{}, err
	}
	a.CreatedAt = time.UnixMilli(created).UTC()
	return a, nil
}

// ListApps returns all apps ordered by name.
func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name, created_at FROM apps ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []App
	for rows.Next() {
		var a App
		var created int64
		if err := rows.Scan(&a.ID, &a.Name, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = time.UnixMilli(created).UTC()
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

// Environments are the accepted events.environment values (D23), checked by
// the schema too. DefaultEnvironment is stored when the client sends none.
var Environments = []string{"production", "testflight", "development"}

// DefaultEnvironment is what an event without environment is stored as:
// SDK 0.1.0 never sent anything but production builds.
const DefaultEnvironment = "production"

// Event is a validated event ready to be stored. App ID and received_at are
// set by the server, never by the client. Empty optional strings are stored
// as NULL; an empty Environment is stored as DefaultEnvironment. Props must
// be a valid JSON object.
type Event struct {
	EventID     string // client-generated UUID, unique per app
	Name        string
	TS          int64 // device time, ms since epoch
	UserID      string
	SessionID   string
	AppVersion  string
	Build       string
	OSVersion   string
	Locale      string
	Region      string
	Storefront  string
	DeviceModel string
	DeviceType  string
	Environment string // production, testflight or development
	Props       string
}

// InsertEvents stores events for appID in a single transaction: either all
// rows are written or none. An event whose (app_id, event_id) already exists,
// in the database or earlier in the same batch, is skipped (a retried
// delivery); duplicates is how many were skipped.
func (s *Store) InsertEvents(ctx context.Context, appID int64, receivedAt time.Time, events []Event) (duplicates int, err error) {
	if len(events) == 0 {
		return 0, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO events
		(app_id, event_id, name, ts, received_at, user_id, session_id, app_version, build, os_version,
		 locale, region, storefront, device_model, device_type, environment, props)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (app_id, event_id) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	recv := receivedAt.UnixMilli()
	for _, e := range events {
		props := e.Props
		if props == "" {
			props = "{}"
		}
		if e.EventID == "" {
			return 0, errors.New("event without event_id")
		}
		env := e.Environment
		if env == "" {
			env = DefaultEnvironment
		}
		res, err := stmt.ExecContext(ctx, appID, e.EventID, e.Name, e.TS, recv, e.UserID, e.SessionID,
			nullIfEmpty(e.AppVersion), nullIfEmpty(e.Build), nullIfEmpty(e.OSVersion),
			nullIfEmpty(e.Locale), nullIfEmpty(e.Region), nullIfEmpty(e.Storefront),
			nullIfEmpty(e.DeviceModel), nullIfEmpty(e.DeviceType), env, props)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n == 0 {
			duplicates++
		}
	}
	return duplicates, tx.Commit()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
