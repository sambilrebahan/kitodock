package ssh

import (
	"path/filepath"
	"testing"
)

func TestHostStoreEmptyWhenAbsent(t *testing.T) {
	s, err := LoadHostStore(filepath.Join(t.TempDir(), "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatalf("List = %v, want empty", s.List())
	}
}

func TestHostStoreCrudRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	s, err := LoadHostStore(path)
	if err != nil {
		t.Fatal(err)
	}

	s.Upsert(Host{Name: "prod", Address: "203.0.113.10", Port: 22, User: "root", AuthMode: "key"})
	s.Upsert(Host{Name: "staging", Address: "203.0.113.11", Port: 2222, User: "deploy", AuthMode: "password", Password: "sealed-blob"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	// Upsert same name replaces instead of duplicating.
	s.Upsert(Host{Name: "prod", Address: "203.0.113.99", Port: 22, User: "root", AuthMode: "key"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadHostStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.List()
	if len(got) != 2 {
		t.Fatalf("List = %v, want 2 hosts", got)
	}
	prod, ok := reloaded.Find("prod")
	if !ok || prod.Address != "203.0.113.99" {
		t.Fatalf("prod after upsert = %+v", prod)
	}
	if !reloaded.Remove("staging") {
		t.Fatal("Remove(staging) = false")
	}
	if reloaded.Remove("staging") {
		t.Fatal("double Remove = true")
	}
	if _, ok := reloaded.Find("staging"); ok {
		t.Fatal("staging still present after Remove")
	}
}
