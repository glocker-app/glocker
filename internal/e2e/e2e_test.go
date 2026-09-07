package e2e

import (
	"bytes"
	"testing"
)

func TestGenerateMasterKey(t *testing.T) {
	a, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	if len(a) != MasterKeyLen {
		t.Fatalf("len = %d, want %d", len(a), MasterKeyLen)
	}
	if bytes.Equal(a, make([]byte, MasterKeyLen)) {
		t.Fatal("master key is all zeros")
	}

	// Two keys must differ — a basic sanity check that it's actually random.
	b, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("GenerateMasterKey: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two generated master keys are identical — not random")
	}
}
