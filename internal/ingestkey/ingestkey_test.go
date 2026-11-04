package ingestkey

import (
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	k1, h1 := Generate()
	k2, h2 := Generate()
	if k1 == k2 || h1 == h2 {
		t.Fatal("two generated keys are equal")
	}
	if !strings.HasPrefix(k1, Prefix) || len(k1) != len(Prefix)+43 {
		t.Fatalf("unexpected key format %q", k1)
	}
	if Hash(k1) != h1 || len(h1) != 64 {
		t.Fatalf("hash mismatch: %q", h1)
	}
	if strings.Contains(h1, k1) {
		t.Fatal("hash contains the key")
	}
}

func TestWellFormed(t *testing.T) {
	for range 100 {
		if k, _ := Generate(); !WellFormed(k) {
			t.Fatalf("generated key %q not well formed", k)
		}
	}
	good := Prefix + strings.Repeat("aZ0-_", 8) + "abc"
	if !WellFormed(good) {
		t.Fatalf("%q should be well formed", good)
	}
	for _, k := range []string{
		"",
		"sk_",
		"sk_nope",
		good[:len(good)-1],
		good + "a",
		"pk_" + good[3:],
		"SK_" + good[3:],
		good[:10] + "+" + good[11:], // standard base64, not url
		good[:10] + "/" + good[11:],
		good[:10] + "=" + good[11:],
		good[:10] + " " + good[11:],
		good[:10] + "\x00" + good[11:],
		good[:44] + "é", // multibyte: right byte length, wrong charset
	} {
		if WellFormed(k) {
			t.Errorf("%q accepted", k)
		}
	}
}
