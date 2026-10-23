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

func TestPlanForProduct(t *testing.T) {
	billing = BillingOptions{ProductID: "annual-prod", ProductIDMonthly: "monthly-prod"}
	t.Cleanup(func() { billing = BillingOptions{} })

	for pid, want := range map[string]string{
		"annual-prod":  "annual",
		"monthly-prod": "monthly",
		"other":        "",
		"":             "",
	} {
		if got := planForProduct(pid); got != want {
			t.Errorf("planForProduct(%q) = %q, want %q", pid, got, want)
		}
	}
	// No empty-product false match even when a product id is unset.
	billing.ProductIDMonthly = ""
	if got := planForProduct(""); got != "" {
		t.Errorf("planForProduct(\"\") with no monthly = %q, want empty", got)
	}
}
