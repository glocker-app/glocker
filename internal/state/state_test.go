package state

import (
	"testing"
	"time"
)

func TestPanicMode(t *testing.T) {
	// Test setting and getting panic until time
	testTime := time.Now().Add(30 * time.Minute)
	SetPanicUntil(testTime)

	got := GetPanicUntil()
	if !got.Equal(testTime) {
		t.Errorf("GetPanicUntil() = %v, want %v", got, testTime)
	}

	// Test last suspend time
	suspendTime := time.Now()
	SetLastSuspendTime(suspendTime)

	gotSuspend := GetLastSuspendTime()
	if !gotSuspend.Equal(suspendTime) {
		t.Errorf("GetLastSuspendTime() = %v, want %v", gotSuspend, suspendTime)
	}
}

func TestEmailRateLimiting(t *testing.T) {
	// Test setting and getting email time
	eventType := "test_event"
	testTime := time.Now()

	SetLastEmailTime(eventType, testTime)

	got, exists := GetLastEmailTime(eventType)
	if !exists {
		t.Error("Expected email time to exist")
	}
	if !got.Equal(testTime) {
		t.Errorf("GetLastEmailTime(%q) = %v, want %v", eventType, got, testTime)
	}

	// Test non-existent event
	_, exists = GetLastEmailTime("non_existent")
	if exists {
		t.Error("Expected non-existent event to not exist")
	}
}

func TestTempUnblocks(t *testing.T) {
	// Clear any existing unblocks
	SetTempUnblocks([]TempUnblock{})

	// Add a temp unblock
	domain := "example.com"
	expiresAt := time.Now().Add(30 * time.Minute)
	AddTempUnblock(domain, expiresAt)

	// Get temp unblocks
	unblocks := GetTempUnblocks()
	if len(unblocks) != 1 {
		t.Fatalf("Expected 1 temp unblock, got %d", len(unblocks))
	}
	if unblocks[0].Domain != domain {
		t.Errorf("Expected domain %q, got %q", domain, unblocks[0].Domain)
	}
	if !unblocks[0].ExpiresAt.Equal(expiresAt) {
		t.Errorf("Expected expiry %v, got %v", expiresAt, unblocks[0].ExpiresAt)
	}

	// Add another
	AddTempUnblock("another.com", time.Now().Add(1*time.Hour))
	unblocks = GetTempUnblocks()
	if len(unblocks) != 2 {
		t.Errorf("Expected 2 temp unblocks, got %d", len(unblocks))
	}

	// Replace all
	newUnblocks := []TempUnblock{
		{Domain: "new.com", ExpiresAt: time.Now().Add(15 * time.Minute)},
	}
	SetTempUnblocks(newUnblocks)
	unblocks = GetTempUnblocks()
	if len(unblocks) != 1 {
		t.Errorf("Expected 1 temp unblock after replacement, got %d", len(unblocks))
	}
	if unblocks[0].Domain != "new.com" {
		t.Errorf("Expected domain 'new.com', got %q", unblocks[0].Domain)
	}
}

func TestSSEClients(t *testing.T) {
	// Create a test channel
	ch1 := make(chan string, 1)
	ch2 := make(chan string, 1)

	// Add clients
	AddSSEClient(ch1)
	AddSSEClient(ch2)

	// Broadcast a message
	BroadcastSSE("test message")

	// Check if both channels received the message
	select {
	case msg := <-ch1:
		if msg != "test message" {
			t.Errorf("Channel 1 got %q, want 'test message'", msg)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Channel 1 did not receive message")
	}

	select {
	case msg := <-ch2:
		if msg != "test message" {
			t.Errorf("Channel 2 got %q, want 'test message'", msg)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Channel 2 did not receive message")
	}

	// Remove a client
	RemoveSSEClient(ch1)

	// Broadcast again
	BroadcastSSE("test message 2")

	// ch1 should not receive, ch2 should
	select {
	case <-ch1:
		t.Error("Channel 1 should not have received message after removal")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}

	select {
	case msg := <-ch2:
		if msg != "test message 2" {
			t.Errorf("Channel 2 got %q, want 'test message 2'", msg)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Channel 2 did not receive message")
	}

	// Clean up
	RemoveSSEClient(ch2)
}

func TestViolations(t *testing.T) {
	// Clear violations
	ClearViolations()

	// Add violations
	v1 := Violation{
		Timestamp: time.Now(),
		Host:      "example.com",
		URL:       "https://example.com/page",
		Type:      "web_access",
	}
	AddViolation(v1)

	violations := GetViolations()
	if len(violations) != 1 {
		t.Fatalf("Expected 1 violation, got %d", len(violations))
	}
	if violations[0].Host != v1.Host {
		t.Errorf("Expected host %q, got %q", v1.Host, violations[0].Host)
	}

	// Add another
	v2 := Violation{
		Timestamp: time.Now(),
		Host:      "another.com",
		URL:       "https://another.com/page",
		Type:      "content_report",
	}
	AddViolation(v2)

	violations = GetViolations()
	if len(violations) != 2 {
		t.Errorf("Expected 2 violations, got %d", len(violations))
	}

	// Test last reset time
	resetTime := GetLastViolationReset()
	if resetTime.IsZero() {
		t.Error("Expected last reset time to be set after ClearViolations")
	}

	// Clear and check
	ClearViolations()
	violations = GetViolations()
	if len(violations) != 0 {
		t.Errorf("Expected 0 violations after clear, got %d", len(violations))
	}

	// Test setting last reset
	newResetTime := time.Now().Add(-1 * time.Hour)
	SetLastViolationReset(newResetTime)
	got := GetLastViolationReset()
	if !got.Equal(newResetTime) {
		t.Errorf("Expected reset time %v, got %v", newResetTime, got)
	}
}
