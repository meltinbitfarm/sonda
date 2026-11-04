// Package ingest defines the JSON contract of POST /v1/events and validates
// events against it (D10, D11, D13). It has no HTTP or database code: it turns
// one raw JSON event into a store.Event or a short rejection reason.
//
// The contract is documented for the SDK in .jerome/notes/ingest-contract.md;
// keep the two in sync.
package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/meltinbitfarm/sonda/internal/store"
)

// Limits of the contract.
const (
	MaxNameLen    = 64                  // D13
	MaxPropsBytes = 2048                // D13, compact JSON encoding
	MaxPropKeyLen = 64                  // same rule as event names
	MaxIDLen      = 36                  // canonical UUID
	MaxFieldLen   = 64                  // app_version, build, os_version, locale, ...
	MaxFuture     = 24 * time.Hour      // ts may be ahead of server time by at most this
	MaxPast       = 31 * 24 * time.Hour // ts may be behind server time by at most this
)

var (
	// D11: area.action, both parts snake_case starting with a letter.
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)
	// Prop keys: snake_case like the D12 catalogue (step_name, product_id).
	propKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	uuidRe    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// DeviceTypes are the accepted device_type values (D10).
var DeviceTypes = map[string]bool{"phone": true, "pad": true, "mac": true}

// Batch is the request body.
type Batch struct {
	Events []json.RawMessage `json:"events"`
}

// wireEvent is one event as sent by the SDK. Pointers distinguish a missing
// required field from a zero value. Unknown fields are ignored (a newer SDK
// may send fields this server does not know yet).
type wireEvent struct {
	EventID     *string         `json:"event_id"`
	Name        *string         `json:"name"`
	TS          *int64          `json:"ts"`
	UserID      *string         `json:"user_id"`
	SessionID   *string         `json:"session_id"`
	AppVersion  string          `json:"app_version"`
	Build       string          `json:"build"`
	OSVersion   string          `json:"os_version"`
	Locale      string          `json:"locale"`
	Region      string          `json:"region"`
	Storefront  string          `json:"storefront"`
	DeviceModel string          `json:"device_model"`
	DeviceType  string          `json:"device_type"`
	Props       json.RawMessage `json:"props"`
}

// Validate checks one raw event against the contract, with now as the
// server's clock. On failure it returns a short reason suitable for the
// response (it never echoes client values).
func Validate(raw json.RawMessage, now time.Time) (store.Event, error) {
	var w wireEvent
	if err := json.Unmarshal(raw, &w); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return store.Event{}, fmt.Errorf("%s: wrong type", te.Field)
		}
		return store.Event{}, errors.New("event is not a JSON object")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return store.Event{}, errors.New("event is not a JSON object")
	}

	eventID, err := uuidField("event_id", w.EventID)
	if err != nil {
		return store.Event{}, err
	}

	switch {
	case w.Name == nil:
		return store.Event{}, errors.New("name: missing")
	case len(*w.Name) > MaxNameLen:
		return store.Event{}, fmt.Errorf("name: longer than %d characters", MaxNameLen)
	case !nameRe.MatchString(*w.Name):
		return store.Event{}, errors.New("name: must be area.action in snake_case")
	}

	if w.TS == nil {
		return store.Event{}, errors.New("ts: missing")
	}
	t := time.UnixMilli(*w.TS)
	if t.After(now.Add(MaxFuture)) {
		return store.Event{}, errors.New("ts: too far in the future")
	}
	if t.Before(now.Add(-MaxPast)) {
		return store.Event{}, errors.New("ts: too old")
	}

	userID, err := uuidField("user_id", w.UserID)
	if err != nil {
		return store.Event{}, err
	}
	sessionID, err := uuidField("session_id", w.SessionID)
	if err != nil {
		return store.Event{}, err
	}

	ev := store.Event{
		EventID:     eventID,
		Name:        *w.Name,
		TS:          *w.TS,
		UserID:      userID,
		SessionID:   sessionID,
		AppVersion:  w.AppVersion,
		Build:       w.Build,
		OSVersion:   w.OSVersion,
		Locale:      w.Locale,
		Region:      w.Region,
		Storefront:  w.Storefront,
		DeviceModel: w.DeviceModel,
		DeviceType:  w.DeviceType,
	}
	for _, f := range []struct{ name, val string }{
		{"app_version", ev.AppVersion}, {"build", ev.Build}, {"os_version", ev.OSVersion},
		{"locale", ev.Locale}, {"region", ev.Region}, {"storefront", ev.Storefront},
		{"device_model", ev.DeviceModel}, {"device_type", ev.DeviceType},
	} {
		if err := checkField(f.name, f.val); err != nil {
			return store.Event{}, err
		}
	}
	if ev.DeviceType != "" && !DeviceTypes[ev.DeviceType] {
		return store.Event{}, errors.New("device_type: must be phone, pad or mac")
	}

	ev.Props, err = validateProps(w.Props)
	if err != nil {
		return store.Event{}, err
	}
	return ev, nil
}

// uuidField requires a UUID and returns it in lowercase canonical form, so
// Swift's uppercase uuidString and lowercase forms are the same user.
// Requiring a UUID also keeps emails or names out of user_id (D10).
func uuidField(name string, v *string) (string, error) {
	if v == nil || *v == "" {
		return "", fmt.Errorf("%s: missing", name)
	}
	s := strings.ToLower(*v)
	if len(s) != MaxIDLen || !uuidRe.MatchString(s) {
		return "", fmt.Errorf("%s: must be a UUID", name)
	}
	return s, nil
}

func checkField(name, v string) error {
	if len(v) > MaxFieldLen {
		return fmt.Errorf("%s: longer than %d bytes", name, MaxFieldLen)
	}
	if !utf8.ValidString(v) || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("%s: contains control characters", name)
	}
	return nil
}

// validateProps accepts a missing or null value (stored as {}) or a flat JSON
// object whose values are strings, numbers, booleans or null. It returns the
// compact encoding, which is what the 2 KB limit is measured on.
func validateProps(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "{}", nil
	}
	if raw[0] != '{' {
		return "", errors.New("props: must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numbers exactly as sent
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return "", errors.New("props: must be a JSON object")
	}
	for k, v := range m {
		if len(k) > MaxPropKeyLen || !propKeyRe.MatchString(k) {
			return "", errors.New("props: keys must be snake_case, at most 64 characters")
		}
		switch v.(type) {
		case map[string]any, []any:
			return "", errors.New("props: nested objects and arrays are not allowed")
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return "", errors.New("props: cannot encode")
	}
	if len(out) > MaxPropsBytes {
		return "", fmt.Errorf("props: larger than %d bytes", MaxPropsBytes)
	}
	return string(out), nil
}
