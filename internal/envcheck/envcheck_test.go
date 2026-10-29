package envcheck

import "testing"

func TestToolPresentAndMissing(t *testing.T) {
	// `sh` is on every POSIX system the tests run on.
	if r := tool("shell", "sh", "impact"); !r.OK {
		t.Error("sh should be found on PATH")
	}
	if r := tool("bogus", "glocker-definitely-not-a-real-binary", "impact"); r.OK {
		t.Error("a nonexistent binary must not report OK")
	} else if r.Detail == "" {
		t.Error("a failing check must carry an impact detail")
	}
}

func TestCheckMarksRequired(t *testing.T) {
	// systemd is always required; visudo only when sudoers management is on.
	for _, r := range Check(true) {
		switch r.Name {
		case "systemd":
			if !r.Required {
				t.Error("systemd must be required")
			}
		case "visudo":
			if !r.Required {
				t.Error("visudo must be required when sudoers is on")
			}
		}
	}
	for _, r := range Check(false) {
		if r.Name == "visudo" && r.Required {
			t.Error("visudo must not be required when sudoers is off")
		}
	}
}

func TestVerifyGatesOnlyOnRequired(t *testing.T) {
	// A missing advisory capability must not block install; a missing required one must.
	if err := Verify([]Result{{Name: "watchdog scheduler", OK: false, Required: false, Detail: "d"}}); err != nil {
		t.Errorf("advisory-missing should not fail Verify: %v", err)
	}
	if err := Verify([]Result{{Name: "systemd", OK: false, Required: true, Detail: "d"}}); err == nil {
		t.Error("required-missing must fail Verify")
	}
	if err := Verify([]Result{{Name: "systemd", OK: true, Required: true}}); err != nil {
		t.Errorf("all-required-present should pass: %v", err)
	}
}

func TestCheckDetailsPresentWhenMissing(t *testing.T) {
	// Every failing probe must carry an impact string so the message is actionable.
	for _, r := range Check(true) {
		if !r.OK && r.Detail == "" {
			t.Errorf("failing check %q has no detail", r.Name)
		}
	}
}
