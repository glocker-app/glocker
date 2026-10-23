package mailer

import (
	"context"
	"testing"

	"glocker/internal/config"
)

func TestDisabledIsNoop(t *testing.T) {
	// Not enabled, and missing domain/key — all should yield a disabled no-op.
	for _, c := range []config.MailConfig{
		{},
		{Enabled: true},                              // no domain/key
		{Enabled: true, Domain: "mg.glockerapp.com"}, // no key
		{Enabled: false, Domain: "mg.glockerapp.com", APIKey: "k"},
	} {
		m := New(c)
		if m.Enabled() {
			t.Errorf("New(%+v) should be disabled", c)
		}
		if err := m.Send(context.Background(), "a@b.com", "s", "t", ""); err != nil {
			t.Errorf("disabled Send should be a no-op, got %v", err)
		}
	}
}

func TestFromDefaultsAndOverride(t *testing.T) {
	m := New(config.MailConfig{Enabled: true, Domain: "mg.glockerapp.com", APIKey: "k"})
	if !m.Enabled() {
		t.Fatal("should be enabled")
	}
	if m.From() != "noreply@mg.glockerapp.com" {
		t.Errorf("default From = %q, want noreply@mg.glockerapp.com", m.From())
	}

	m2 := New(config.MailConfig{Enabled: true, Domain: "mg.glockerapp.com", APIKey: "k", From: "verify@mg.glockerapp.com"})
	if m2.From() != "verify@mg.glockerapp.com" {
		t.Errorf("explicit From = %q", m2.From())
	}
}

func TestFromHeaderDisplayName(t *testing.T) {
	// Default display name.
	m := New(config.MailConfig{Enabled: true, Domain: "mg.glockerapp.com", APIKey: "k"})
	if m.fromHeader != "Glocker <noreply@mg.glockerapp.com>" {
		t.Errorf("default fromHeader = %q", m.fromHeader)
	}
	// Overridden display name.
	m2 := New(config.MailConfig{Enabled: true, Domain: "mg.glockerapp.com", APIKey: "k", From: "noreply@mg.glockerapp.com", FromName: "Acme"})
	if m2.fromHeader != "Acme <noreply@mg.glockerapp.com>" {
		t.Errorf("overridden fromHeader = %q", m2.fromHeader)
	}
	// A From already carrying a name is left as-is.
	m3 := New(config.MailConfig{Enabled: true, Domain: "mg.glockerapp.com", APIKey: "k", From: "Team <team@mg.glockerapp.com>"})
	if m3.fromHeader != "Team <team@mg.glockerapp.com>" {
		t.Errorf("preformed fromHeader = %q", m3.fromHeader)
	}
}

func TestRegionEUConstructs(t *testing.T) {
	// Just ensure the EU path builds a usable, enabled mailer (no network call).
	m := New(config.MailConfig{Enabled: true, Domain: "mg.glockerapp.com", APIKey: "k", Region: "EU"})
	if !m.Enabled() {
		t.Error("EU mailer should be enabled")
	}
}
