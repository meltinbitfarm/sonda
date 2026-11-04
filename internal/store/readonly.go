package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// MaxValueLength caps any string or blob built by a statement on a
// connection taken with LimitedConn (SQLITE_LIMIT_LENGTH; SQLite's default is
// 1 GB). zeroblob, randomblob, printf, hex, replace, || and group_concat all
// stop with "string or blob too big" past it, so one query tool call cannot
// allocate hundreds of MB (O6 M2). Stored values are far smaller: a prop
// value is at most 2 KB, a request body 256 KB.
const MaxValueLength = 16 << 20

// LimitedConn takes a connection from db (the read-only pool) and caps the
// length of strings and blobs on it to MaxValueLength. Run untrusted SQL on
// the returned connection and Close it afterwards to give it back to the
// pool. The cap stays on the connection, which is harmless for the other
// tools; it is set again on every call because the driver offers no hook to
// set it when a connection is opened.
func LimitedConn(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := sqlite.Limit(c, sqlite3.SQLITE_LIMIT_LENGTH, MaxValueLength); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// OpenReadOnly opens a separate connection pool on an existing database for
// the MCP tools (D14). Writes are blocked three times over:
//
//  1. the file is opened with mode=ro (SQLITE_OPEN_READONLY);
//  2. PRAGMA query_only=1 on every connection;
//  3. an SQLite authorizer, installed on every connection by a private
//     driver, allows only reading: SELECT, reading columns, functions
//     (minus a short deny list), recursive CTEs and a few schema
//     introspection pragmas (so pragma_table_info('events') works). ATTACH,
//     other pragmas, transactions, DDL and DML are refused at prepare time.
//     apps.ingest_key_hash reads as NULL: the MCP side never needs it.
//
// It does not create or migrate the database: open it with Open first.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	for _, p := range []string{"busy_timeout(5000)", "query_only(1)"} {
		q.Add("_pragma", p)
	}
	drv := &sqlite.Driver{}
	drv.RegisterConnectionHook(func(c sqlite.ExecQuerierContext, _ string) error {
		r, ok := c.(sqlite.AuthorizerRegisterer)
		if !ok {
			return errors.New("sqlite driver does not support an authorizer")
		}
		return r.RegisterAuthorizer(readOnlyAuthorizer)
	})
	db := sql.OpenDB(connector{drv: drv, dsn: "file:" + path + "?" + q.Encode()})
	db.SetMaxOpenConns(4)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

type connector struct {
	drv *sqlite.Driver
	dsn string
}

func (c connector) Connect(context.Context) (driver.Conn, error) { return c.drv.Open(c.dsn) }
func (c connector) Driver() driver.Driver                        { return c.drv }

// deniedFunctions are SQL functions refused even on a read-only connection:
// extension loading and file access (absent from this build, denied in case
// a future build adds them) and the FTS3 tokenizer pointer trick.
var deniedFunctions = map[string]bool{
	"load_extension": true,
	"fts3_tokenizer": true,
	"readfile":       true,
	"writefile":      true,
	"edit":           true,
}

// introspectionPragmas have no side effects; they are allowed so the query
// tool can describe the schema, e.g. SELECT * FROM pragma_table_info('events').
var introspectionPragmas = map[string]bool{
	"table_info":       true,
	"table_xinfo":      true,
	"table_list":       true,
	"index_list":       true,
	"index_info":       true,
	"index_xinfo":      true,
	"foreign_key_list": true,
}

func readOnlyAuthorizer(action sqlite.AuthorizerActionCode, arg1, arg2, _, _ string) sqlite.AuthorizerReturnCode {
	switch action {
	case sqlite.AuthRead:
		if strings.EqualFold(arg1, "apps") && strings.EqualFold(arg2, "ingest_key_hash") {
			return sqlite.AuthorizerIgnore // the column reads as NULL
		}
		return sqlite.AuthorizerOK
	case sqlite.AuthSelect, sqlite.AuthRecursive:
		return sqlite.AuthorizerOK
	case sqlite.AuthPragma:
		if introspectionPragmas[strings.ToLower(arg1)] {
			return sqlite.AuthorizerOK
		}
		return sqlite.AuthorizerDeny
	case sqlite.AuthFunction:
		if deniedFunctions[strings.ToLower(arg2)] {
			return sqlite.AuthorizerDeny
		}
		return sqlite.AuthorizerOK
	}
	return sqlite.AuthorizerDeny
}
