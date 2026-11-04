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
