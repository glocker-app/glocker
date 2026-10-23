package stats

import "testing"

func TestCheckoutProduct(t *testing.T) {
	billing = BillingOptions{ProductID: "annual-prod", ProductIDMonthly: "monthly-prod"}
	t.Cleanup(func() { billing = BillingOptions{} })

	for plan, want := range map[string]string{
		"monthly": "monthly-prod",
		"annual":  "annual-prod",
		"":        "annual-prod", // default
		"bogus":   "annual-prod", // unknown plan → annual
	} {
		if got := checkoutProduct(plan); got != want {
			t.Errorf("checkoutProduct(%q) = %q, want %q", plan, got, want)
		}
	}

	// With no monthly product configured, "monthly" falls back to annual.
	billing.ProductIDMonthly = ""
	if got := checkoutProduct("monthly"); got != "annual-prod" {
		t.Errorf("monthly fallback = %q, want annual-prod", got)
	}
}
