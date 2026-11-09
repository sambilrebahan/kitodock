package applicationcontext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureAndLog(t *testing.T) {
	store := t.TempDir()
	run := func(dir, name string, args ...string) (string, error) {
		if name != "php" || args[0] != "artisan" {
			return "", fmt.Errorf("unexpected %s %v", name, args)
		}
		switch args[1] {
		case "route:list":
			return `[{"method":"GET","uri":"\/health"},{"method":"POST","uri":"\/checkout"}]`, nil
		case "about":
			return `{"environment":"local","laravel":"11.9"}`, nil
		}
		return "", fmt.Errorf("no such command")
	}

	snap, err := Capture("shop", `C:\www\shop`, "php", run)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Provider != "laravel" || len(snap.Routes) != 2 || snap.Routes[1]["uri"] != "/checkout" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.About["environment"] != "local" {
		t.Fatalf("about = %+v", snap.About)
	}

	path, err := AppendLog(store, "shop", snap)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "shop.jsonl" {
		t.Fatalf("log name = %s", path)
	}
	snaps, err := ReadLog(store, "shop", 0)
	if err != nil || len(snaps) != 1 || snaps[0].Routes[0]["uri"] != "/health" {
		t.Fatalf("read back = %+v err=%v", snaps, err)
	}

	// Failing probes still produce a snapshot with the error recorded.
	broken, err := Capture("shop", "dir", "php", func(string, string, ...string) (string, error) {
		return "", fmt.Errorf("no php")
	})
	if err != nil {
		t.Fatal(err)
	}
	if broken.Error == "" || broken.Routes != nil {
		t.Fatalf("broken snapshot = %+v", broken)
	}
	if _, err := AppendLog(store, "shop", broken); err != nil {
		t.Fatal(err)
	}
	if snaps, _ = ReadLog(store, "shop", 0); len(snaps) != 2 {
		t.Fatalf("log = %d entries", len(snaps))
	}
}

func TestMailpitList(t *testing.T) {
	payload := `{"messages":[
		{"ID":"a1","From":{"Address":"shop@example.com","Name":"Shop"},"To":[{"Address":"buyer@example.com"}],"Subject":"Order confirmed","Created":"2026-11-09T10:00:00Z"},
		{"ID":"b2","From":{"Address":"no-reply@kitodock.test","Name":""},"To":[{"Address":"admin@mailpit.test","Name":""}],"Subject":"Test mail","Created":"2026-11-09T11:00:00Z"}
	]}`
	fetch := &FetchMailpit{
		BaseURL: "http://127.0.0.1:8025",
		HTTPGet: func(url string) ([]byte, error) {
			if !strings.Contains(url, "/api/v1/messages") {
				return nil, fmt.Errorf("wrong url %s", url)
			}
			return []byte(payload), nil
		},
	}
	mails, err := fetch.List(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mails) != 2 {
		t.Fatalf("mails = %+v", mails)
	}
	if mails[0].From != "Shop <shop@example.com>" || mails[0].Subject != "Order confirmed" {
		t.Fatalf("first mail = %+v", mails[0])
	}
	// Project tag derives from the recipient local-part.
	if mails[1].Tag != "admin" {
		t.Fatalf("tag = %q", mails[1].Tag)
	}

	// Empty baseURL errors instead of guessing.
	if _, err := (&FetchMailpit{}).List(0); err == nil {
		t.Fatal("empty mailpit accepted")
	}
}

func TestCaptureRequiresDir(t *testing.T) {
	if _, err := Capture("x", "", "php", ExecRunner); err == nil {
		t.Fatal("capture without dir accepted")
	}
}

var _ = json.Marshal
var _ = os.ReadFile
