package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
)

var (
	ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	// Bracketed ([2001:db8::7], [::]) or bare with two or more colons and a
	// final hex group (::1, fe80::1%en0). May also catch a few harmless
	// non-IP strings such as 12:00:00; this is a log, over-redaction is fine.
	ipv6Re = regexp.MustCompile(`(?i)\[[0-9a-f.%]*:[0-9a-z:.%]*\]|(?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{1,4}(?:%[0-9a-z]+)?`)
)

// RedactIPs replaces anything that looks like an IPv4 or IPv6 address with "[ip]".
func RedactIPs(s string) string {
	s = ipv4Re.ReplaceAllString(s, "[ip]")
	return ipv6Re.ReplaceAllString(s, "[ip]")
}

// NewLogger returns the text logger used by the server, which redacts IP
// addresses from messages and attribute values (D9). Use it for
// http.Server.ErrorLog too (slog.NewLogLogger(l.Handler(), ...)).
func NewLogger(w io.Writer) *slog.Logger {
	return slog.New(redactHandler{slog.NewTextHandler(w, nil)})
}

type redactHandler struct{ h slog.Handler }

func (r redactHandler) Enabled(ctx context.Context, l slog.Level) bool { return r.h.Enabled(ctx, l) }

func (r redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, RedactIPs(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return r.h.Handle(ctx, out)
}

func (r redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(as))
	for i, a := range as {
		red[i] = redactAttr(a)
	}
	return redactHandler{r.h.WithAttrs(red)}
}

func (r redactHandler) WithGroup(name string) slog.Handler { return redactHandler{r.h.WithGroup(name)} }

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, RedactIPs(v.String()))
	case slog.KindAny:
		return slog.String(a.Key, RedactIPs(fmt.Sprint(v.Any())))
	case slog.KindGroup:
		g := v.Group()
		red := make([]any, len(g))
		for i, ga := range g {
			red[i] = redactAttr(ga)
		}
		return slog.Group(a.Key, red...)
	}
	return slog.Attr{Key: a.Key, Value: v}
}
