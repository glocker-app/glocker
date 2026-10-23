package store

import "testing"

func TestSetSubscriptionProduct(t *testing.T) {
	db := openTestDB(t)
	u, err := db.createUser("p@x.com", "pw123456", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSubscriptionProduct(u.ID, "prod-123"); err != nil {
		t.Fatal(err)
	}
	got, err := db.UserByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SubProductID != "prod-123" {
		t.Errorf("SubProductID = %q, want prod-123", got.SubProductID)
	}
	// An empty id is a no-op — it must not wipe a recorded product.
	if err := db.SetSubscriptionProduct(u.ID, ""); err != nil {
		t.Fatal(err)
	}
	got2, _ := db.UserByID(u.ID)
	if got2.SubProductID != "prod-123" {
		t.Errorf("empty should be a no-op, got %q", got2.SubProductID)
	}
}
