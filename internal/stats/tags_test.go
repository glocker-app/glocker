package stats

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type apiTag struct {
	ID          uint   `json:"id"`
	Scope       string `json:"scope"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Distraction bool   `json:"distraction"`
	Behavior    string `json:"behavior"`
}

func TestTagsRegistryAPI(t *testing.T) {
	mux, sdb := newMux(t)
	_, alice, _ := account(t, sdb, "alice@x.com")
	_, bob, _ := account(t, sdb, "bob@x.com")

	list := func(cookie *http.Cookie) []apiTag {
		r := httptest.NewRequest("GET", "/api/tags", nil)
		r.AddCookie(cookie)
		var out struct {
			Tags []apiTag `json:"tags"`
		}
		json.Unmarshal(do(mux, r).Body.Bytes(), &out)
		return out.Tags
	}
	find := func(tags []apiTag, ns, name string) *apiTag {
		for i := range tags {
			if tags[i].Namespace == ns && tags[i].Name == name {
				return &tags[i]
			}
		}
		return nil
	}
	send := func(method, url, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r.AddCookie(cookie)
		return do(mux, r)
	}

	// A fresh account sees the shared Glocker system tags.
	tags := list(alice)
	ent := find(tags, "Glocker", "entertainment")
	if ent == nil || ent.Scope != "system" || !ent.Distraction {
		t.Fatalf("expected system Glocker:entertainment distraction=true, got %+v", ent)
	}

	// Create a user tag.
	if rec := send("POST", "/api/tags", `{"namespace":"Activity","name":"reading","color":"#2f9e8f","distraction":false}`, alice); rec.Code != http.StatusCreated {
		t.Fatalf("create tag: %d %s", rec.Code, rec.Body.String())
	}
	if find(list(alice), "Activity", "reading") == nil {
		t.Fatal("created tag not listed")
	}
	// Can't create in the system namespace, and duplicates conflict.
	if rec := send("POST", "/api/tags", `{"namespace":"Glocker","name":"x"}`, alice); rec.Code != http.StatusBadRequest {
		t.Errorf("Glocker-namespace create should be 400, got %d", rec.Code)
	}
	if rec := send("POST", "/api/tags", `{"namespace":"Activity","name":"reading"}`, alice); rec.Code != http.StatusConflict {
		t.Errorf("duplicate create should be 409, got %d", rec.Code)
	}

	// Flip a system tag's distraction — recorded as alice's override, not shared.
	entID := find(list(alice), "Glocker", "entertainment").ID
	if rec := send("PATCH", "/api/tags?id="+itoa(entID), `{"distraction":false}`, alice); rec.Code != http.StatusNoContent {
		t.Fatalf("override system distraction: %d %s", rec.Code, rec.Body.String())
	}
	if find(list(alice), "Glocker", "entertainment").Distraction {
		t.Error("alice's entertainment should now be non-distraction")
	}
	if !find(list(bob), "Glocker", "entertainment").Distraction {
		t.Error("bob's entertainment must be unaffected by alice's override")
	}
	// System tags can't be deleted.
	if rec := send("DELETE", "/api/tags?id="+itoa(entID), "", alice); rec.Code != http.StatusForbidden {
		t.Errorf("deleting a system tag should be 403, got %d", rec.Code)
	}

	// Edit + delete a user tag.
	rid := find(list(alice), "Activity", "reading").ID
	if rec := send("PATCH", "/api/tags?id="+itoa(rid), `{"namespace":"Activity","name":"reading","color":"#ff4d4d","distraction":true}`, alice); rec.Code != http.StatusNoContent {
		t.Fatalf("edit user tag: %d %s", rec.Code, rec.Body.String())
	}
	if tg := find(list(alice), "Activity", "reading"); tg == nil || !tg.Distraction || tg.Color != "#ff4d4d" {
		t.Errorf("user tag edit didn't persist: %+v", tg)
	}
	if rec := send("DELETE", "/api/tags?id="+itoa(rid), "", alice); rec.Code != http.StatusNoContent {
		t.Fatalf("delete user tag: %d", rec.Code)
	}
	if find(list(alice), "Activity", "reading") != nil {
		t.Error("deleted user tag still listed")
	}
	// Bob never saw alice's user tag.
	if find(list(bob), "Activity", "reading") != nil {
		t.Error("bob should not see alice's user tag")
	}
}

func itoa(u uint) string {
	if u == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}
