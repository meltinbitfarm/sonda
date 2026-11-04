package analytics

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/meltinbitfarm/sonda/internal/store"
)

const (
	// DefaultQueryTimeout bounds one query tool call.
	DefaultQueryTimeout = 10 * time.Second
	defaultQueryRows    = 200
	maxQueryRows        = 1000
	maxQueryLen         = 20_000
	maxCellLen          = 4096
)

// QueryInput is the input of query.
type QueryInput struct {
	SQL     string `json:"sql" jsonschema:"one read-only SQLite statement starting with SELECT or WITH; no trailing statements"`
	MaxRows int    `json:"max_rows,omitempty" jsonschema:"max rows returned, default 200, max 1000"`
}

// QueryResult is the output of query.
type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"row_count"`
	Truncated bool     `json:"truncated"`
}

// writeKeywords are refused anywhere outside literals and quoted
// identifiers. They only improve the error message: the read-only
// connection (mode=ro, query_only, authorizer) is what enforces it.
// REPLACE is missing on purpose: it is also the replace() function.
var writeKeywords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true,
	"CREATE": true, "DROP": true, "ALTER": true, "ATTACH": true, "DETACH": true,
	"PRAGMA": true, "VACUUM": true, "REINDEX": true, "ANALYZE": true,
}

// CheckSelect validates that sql is a single SELECT or WITH statement and
// returns it without the optional trailing semicolon.
func CheckSelect(sql string) (string, error) {
	if len(sql) > maxQueryLen {
		return "", fmt.Errorf("query longer than %d characters", maxQueryLen)
	}
	toks, err := lexSQL(sql)
	if err != nil {
		return "", err
	}
	if len(toks) == 0 {
		return "", errors.New("empty query")
	}
	first := strings.ToUpper(toks[0].text)
	if toks[0].kind != tokWord || (first != "SELECT" && first != "WITH") {
		return "", errors.New("only SELECT or WITH queries are allowed")
	}
	end := len(sql)
	for i, t := range toks {
		if t.kind == tokSemicolon {
			if i != len(toks)-1 {
				return "", errors.New("only one statement is allowed")
			}
			end = t.pos
			continue
		}
		if t.kind == tokWord && writeKeywords[strings.ToUpper(t.text)] {
			return "", fmt.Errorf("%s is not allowed: the database is read-only", strings.ToUpper(t.text))
		}
	}
	return strings.TrimSpace(sql[:end]), nil
}

type tokKind int

const (
	tokWord tokKind = iota
	tokSemicolon
	tokOther // literals, quoted identifiers, numbers, operators
)

type token struct {
	kind tokKind
	text string
	pos  int
}

// lexSQL splits SQLite SQL into the tokens CheckSelect needs, skipping
// whitespace and comments and keeping string literals and quoted
// identifiers opaque.
func lexSQL(s string) ([]token, error) {
	var toks []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				i = len(s) // SQLite accepts an unterminated trailing comment
			} else {
				i += j + 4
			}
		case c == '\'' || c == '"' || c == '`' || c == '[':
			closer := c
			if c == '[' {
				closer = ']'
			}
			j := i + 1
			for {
				k := strings.IndexByte(s[j:], closer)
				if k < 0 {
					return nil, errors.New("unterminated quoted string or identifier")
				}
				j += k + 1
				// Doubled quote = escaped quote (not for [...]).
				if closer != ']' && j < len(s) && s[j] == closer {
					j++
					continue
				}
				break
			}
			toks = append(toks, token{tokOther, s[i:j], i})
			i = j
		case c == ';':
			toks = append(toks, token{tokSemicolon, ";", i})
			i++
		case isWordByte(c) && !(c >= '0' && c <= '9'):
			j := i
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			toks = append(toks, token{tokWord, s[i:j], i})
			i = j
		default:
			j := i + 1
			if c >= '0' && c <= '9' {
				for j < len(s) && isWordByte(s[j]) {
					j++
				}
			}
			toks = append(toks, token{tokOther, s[i:j], i})
			i = j
		}
	}
	return toks, nil
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// Query runs a read-only statement with a timeout and a row limit. The
// Service's DB must be the read-only store (store.OpenReadOnly).
func (s *Service) Query(ctx context.Context, in QueryInput) (QueryResult, error) {
	q, err := CheckSelect(in.SQL)
	if err != nil {
		return QueryResult{}, err
	}
	limit := clamp(in.MaxRows, defaultQueryRows, maxQueryRows)
	timeout := s.QueryTimeout
	if timeout <= 0 {
		timeout = DefaultQueryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Untrusted SQL runs on a connection with capped string/blob length (O6 M2).
	conn, err := store.LimitedConn(ctx, s.DB)
	if err != nil {
		return QueryResult{}, queryErr(ctx, timeout, err)
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return QueryResult{}, queryErr(ctx, timeout, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return QueryResult{}, err
	}
	res := QueryResult{Columns: cols, Rows: [][]any{}}
	for rows.Next() {
		if len(res.Rows) == limit {
			res.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return QueryResult{}, queryErr(ctx, timeout, err)
		}
		for i, v := range vals {
			vals[i] = cell(v)
		}
		res.Rows = append(res.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, queryErr(ctx, timeout, err)
	}
	res.RowCount = len(res.Rows)
	return res, nil
}

func queryErr(ctx context.Context, timeout time.Duration, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("query interrupted after %s; add filters (app_id, ts range) or aggregate", timeout)
	}
	if strings.Contains(err.Error(), "string or blob too big") {
		return fmt.Errorf("a value built by the query exceeds %d MiB: %w", store.MaxValueLength>>20, err)
	}
	return err
}

// cell makes a value JSON-friendly: blobs become text if valid UTF-8,
// otherwise "base64:<...>"; long text is cut to 4096 bytes with a marker.
func cell(v any) any {
	var s string
	switch x := v.(type) {
	case []byte:
		if !utf8.Valid(x) {
			if len(x) > maxCellLen {
				return fmt.Sprintf("base64:%s…[%d bytes]", base64.StdEncoding.EncodeToString(x[:maxCellLen]), len(x))
			}
			return "base64:" + base64.StdEncoding.EncodeToString(x)
		}
		s = string(x)
	case string:
		s = x
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	default:
		return v
	}
	if len(s) > maxCellLen {
		cut := maxCellLen
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return fmt.Sprintf("%s…[truncated, %d bytes]", s[:cut], len(s))
	}
	return s
}
