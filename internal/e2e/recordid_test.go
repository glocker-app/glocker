package e2e

import "testing"

// RecordID is the dedup key that survives per-sync re-encryption. Its contract:
// deterministic for equal inputs, distinct for different inputs, keyed so the
// server can't reverse it, and defined in both keyed (E2E) and unkeyed (plaintext)
// modes.
func TestRecordIDDeterministicAndKeyed(t *testing.T) {
	k1 := []byte("00000000000000000000000000000001")
	k2 := []byte("00000000000000000000000000000002")

	// Deterministic: same key + same parts -> same id.
	a := RecordID(k1, "violation", "1000", "porn", "https://x/y")
	b := RecordID(k1, "violation", "1000", "porn", "https://x/y")
	if a != b {
		t.Fatal("same inputs must give the same RecordID")
	}

	// Different content -> different id.
	if RecordID(k1, "violation", "1000", "porn", "https://x/y") ==
		RecordID(k1, "violation", "1000", "porn", "https://x/z") {
		t.Error("different URL must change the RecordID")
	}

	// Different kind at otherwise-equal fields -> different id (no cross-type
	// collision).
	if RecordID(k1, "violation", "1000", "d") == RecordID(k1, "unblock", "1000", "d") {
		t.Error("different kind must change the RecordID")
	}

	// Keyed: the same content under a different key yields a different id (so the
	// server, without the key, can't precompute ids for guessed content).
	if RecordID(k1, "unblock", "1000", "reddit.com") ==
		RecordID(k2, "unblock", "1000", "reddit.com") {
		t.Error("different key must change the RecordID")
	}

	// Field-boundary safety: the separator prevents ("a","bc") from colliding with
	// ("ab","c").
	if RecordID(k1, "a", "bc") == RecordID(k1, "ab", "c") {
		t.Error("field boundaries must be unambiguous")
	}
}

// In plaintext mode (nil key) RecordID is a plain digest: still deterministic and
// distinct, and — by construction — different from the keyed id for the same
// content.
func TestRecordIDUnkeyedMode(t *testing.T) {
	if RecordID(nil, "violation", "1", "a") != RecordID(nil, "violation", "1", "a") {
		t.Fatal("unkeyed RecordID must be deterministic")
	}
	if RecordID(nil, "violation", "1", "a") == RecordID(nil, "violation", "1", "b") {
		t.Error("unkeyed RecordID must distinguish content")
	}
	key := []byte("00000000000000000000000000000001")
	if RecordID(nil, "violation", "1", "a") == RecordID(key, "violation", "1", "a") {
		t.Error("keyed and unkeyed ids must differ for the same content")
	}
}
