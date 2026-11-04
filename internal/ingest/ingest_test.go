package ingest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

const (
	uid = "0F8FAD5B-D9CB-469F-A165-70867728950E"
	sid = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

// event builds a valid event and applies overrides (nil value = delete the field).
func event(over map[string]any) json.RawMessage {
	m := map[string]any{
		"event_id":     "3F2504E0-4F89-11D3-9A0C-0305E82C3301",
		"name":         "screen.viewed",
		"ts":           now.UnixMilli() - 1000,
		"user_id":      uid,
		"session_id":   sid,
		"app_version":  "1.2.0",
		"build":        "42",
		"os_version":   "26.1",
		"locale":       "it_IT",
		"region":       "IT",
		"storefront":   "ITA",
		"device_model": "iPhone16,2",
		"device_type":  "phone",
		"props":        map[string]any{"screen": "home"},
	}
	for k, v := range over {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func TestValidateOK(t *testing.T) {
	ev, err := Validate(event(nil), now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.EventID != "3f2504e0-4f89-11d3-9a0c-0305e82c3301" || ev.UserID != strings.ToLower(uid) || ev.SessionID != sid || ev.Props != `{"screen":"home"}` || ev.DeviceModel != "iPhone16,2" {
		t.Fatalf("event = %+v", ev)
	}

	// Only required fields; props absent or null become {}.
	for _, props := range []any{nil, json.RawMessage("null")} {
		raw := event(map[string]any{"app_version": nil, "build": nil, "os_version": nil, "locale": nil,
			"region": nil, "storefront": nil, "device_model": nil, "device_type": nil, "props": props})
		ev, err := Validate(raw, now)
		if err != nil || ev.Props != "{}" || ev.Storefront != "" {
			t.Fatalf("minimal event: %+v, %v", ev, err)
		}
	}

	// Flat props with all scalar types, numbers kept as sent; unknown fields ignored.
	raw := event(map[string]any{"props": json.RawMessage(`{"a":"x","b":1.50,"c":true,"d":null,"e":12345678901234567890}`), "future_field": 1})
	ev, err = Validate(raw, now)
	if err != nil || ev.Props != `{"a":"x","b":1.50,"c":true,"d":null,"e":12345678901234567890}` {
		t.Fatalf("scalar props: %q, %v", ev.Props, err)
	}

	// ts at the edges of the window.
	for _, ts := range []int64{now.Add(MaxFuture).UnixMilli(), now.Add(-MaxPast).UnixMilli()} {
		if _, err := Validate(event(map[string]any{"ts": ts}), now); err != nil {
			t.Errorf("ts %d: %v", ts, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	bigProps := map[string]any{"k": strings.Repeat("x", MaxPropsBytes)}
	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"not object", json.RawMessage(`[1]`), "not a JSON object"},
		{"null event", json.RawMessage(`null`), "not a JSON object"},
		{"string event", json.RawMessage(`"x"`), "not a JSON object"},
		{"missing event_id", event(map[string]any{"event_id": nil}), "event_id: missing"},
		{"event_id not uuid", event(map[string]any{"event_id": "1"}), "event_id: must be a UUID"},
		{"missing name", event(map[string]any{"name": nil}), "name: missing"},
		{"name wrong type", event(map[string]any{"name": 5}), "name: wrong type"},
		{"name too long", event(map[string]any{"name": "a." + strings.Repeat("b", 63)}), "name: longer"},
		{"name no area", event(map[string]any{"name": "launched"}), "area.action"},
		{"name camel", event(map[string]any{"name": "app.firstLaunch"}), "area.action"},
		{"name three parts", event(map[string]any{"name": "a.b.c"}), "area.action"},
		{"name space", event(map[string]any{"name": "app.first launch"}), "area.action"},
		{"name leading digit", event(map[string]any{"name": "1app.x"}), "area.action"},
		{"missing ts", event(map[string]any{"ts": nil}), "ts: missing"},
		{"ts string", event(map[string]any{"ts": "2026-11-04"}), "ts: wrong type"},
		{"ts float", event(map[string]any{"ts": 1.5}), "ts: wrong type"},
		{"ts seconds", event(map[string]any{"ts": now.Unix()}), "ts: too old"},
		{"ts future", event(map[string]any{"ts": now.Add(MaxFuture + time.Second).UnixMilli()}), "future"},
		{"ts old", event(map[string]any{"ts": now.Add(-MaxPast - time.Second).UnixMilli()}), "too old"},
		{"missing user", event(map[string]any{"user_id": nil}), "user_id: missing"},
		{"empty session", event(map[string]any{"session_id": ""}), "session_id: missing"},
		{"user not uuid", event(map[string]any{"user_id": "mario@example.com"}), "user_id: must be a UUID"},
		{"long field", event(map[string]any{"build": strings.Repeat("1", 65)}), "build: longer"},
		{"control char", event(map[string]any{"locale": "it\nIT"}), "locale: contains control"},
		{"device type", event(map[string]any{"device_type": "watch"}), "device_type"},
		{"field wrong type", event(map[string]any{"region": 39}), "region: wrong type"},
		{"props array", event(map[string]any{"props": []any{1}}), "props: must be a JSON object"},
		{"props string", event(map[string]any{"props": "x"}), "props: must be a JSON object"},
		{"props nested object", event(map[string]any{"props": map[string]any{"a": map[string]any{"b": 1}}}), "nested"},
		{"props nested array", event(map[string]any{"props": map[string]any{"a": []any{1}}}), "nested"},
		{"props bad key", event(map[string]any{"props": map[string]any{"Screen Name": "x"}}), "keys"},
		{"props too big", event(map[string]any{"props": bigProps}), "larger than 2048"},
	}
	for _, c := range cases {
		_, err := Validate(c.raw, now)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestReasonsDoNotEchoValues(t *testing.T) {
	_, err := Validate(event(map[string]any{"user_id": "secret@example.com"}), now)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
}
