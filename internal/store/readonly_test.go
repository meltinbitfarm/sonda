package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenReadOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateApp(ctx, "Demo", "h1", time.Now()); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()

	var name string
	if err := ro.DB.QueryRowContext(ctx, "SELECT name FROM apps").Scan(&name); err != nil || name != "Demo" {
		t.Fatalf("select = %q, %v", name, err)
	}
	// Reading with functions, CTEs and recursive CTEs is allowed.
	var n int
	if err := ro.DB.QueryRowContext(ctx, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 5)
		SELECT sum(x) + length(json_extract('{"a":"bc"}', '$.a')) FROM c`).Scan(&n); err != nil || n != 17 {
		t.Fatalf("recursive cte = %d, %v", n, err)
	}
	// Schema introspection pragmas are allowed as table-valued functions.
	if err := ro.DB.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('events')").Scan(&n); err != nil || n < 10 {
		t.Fatalf("pragma_table_info = %d, %v", n, err)
	}

	attachPath := filepath.Join(dir, "evil.db")
	for _, stmt := range []string{
		"INSERT INTO apps (name, ingest_key_hash, created_at) VALUES ('x', 'h2', 0)",
		"UPDATE apps SET name = 'y'",
		"DELETE FROM apps",
		"CREATE TABLE t (x)",
		"CREATE TEMP TABLE t (x)",
		"DROP TABLE apps",
		"PRAGMA query_only = 0",
		"PRAGMA journal_mode = DELETE",
		"PRAGMA user_version = 99",
		"ATTACH DATABASE '" + attachPath + "' AS evil",
		"BEGIN IMMEDIATE",
		"VACUUM",
		"SELECT load_extension('x')",
	} {
		if _, err := ro.DB.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s: no error on read-only store", stmt)
		}
	}
	if m, _ := filepath.Glob(attachPath + "*"); len(m) != 0 {
		t.Errorf("ATTACH created %v", m)
	}
	// Nothing changed, as seen from the writer.
	var count, ver int
	st.DB.QueryRowContext(ctx, "SELECT count(*) FROM apps WHERE name = 'Demo'").Scan(&count)
	st.DB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver)
	if count != 1 || ver != 1 {
		t.Fatalf("after attempts: apps=%d user_version=%d", count, ver)
	}

	// The writer still works while the read-only pool is open.
	if _, err := st.CreateApp(ctx, "Other", "h3", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ro.DB.QueryRowContext(ctx, "SELECT count(*) FROM apps").Scan(&count); err != nil || count != 2 {
		t.Fatalf("ro sees %d apps, %v", count, err)
	}
}

func TestOpenReadOnlyMissingFile(t *testing.T) {
	_, err := OpenReadOnly(context.Background(), filepath.Join(t.TempDir(), "none.db"))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unable to open") {
		t.Fatalf("err = %v", err)
	}
}
