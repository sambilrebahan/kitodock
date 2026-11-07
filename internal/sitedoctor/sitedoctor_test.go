package sitedoctor

import (
	"os"
	"path/filepath"
	"testing"
)

func laravelTarget(t *testing.T) (Target, string) {
	t.Helper()
	dir := t.TempDir()
	public := filepath.Join(dir, "public")
	os.MkdirAll(public, 0o755)
	// Minimal artifacts so the checks exercise their happy/sad paths.
	os.WriteFile(filepath.Join(dir, "artisan"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("APP_KEY=\n"), 0o644)
	return Target{
		Name: "shop", Path: dir, Kind: "laravel", DocRoot: public,
		HasVendor: false, HasNodeModules: false,
		HostsEntryPresent: false, FpmExpected: true, FpmRunning: false,
	}, dir
}

func TestFreshLaravelDiagnosesEverything(t *testing.T) {
	target, _ := laravelTarget(t)
	report := Check(target, Dependencies{})

	byCheck := map[string]Finding{}
	for _, f := range report.Findings {
		byCheck[f.CheckID] = f
	}

	// A fresh Laravel project should flag: vendor, .env, APP_KEY, storage,
	// hosts, php-fpm.
	for _, checkID := range []string{"vendor", "env_file", "app_key", "storage_link", "hosts", "php_fpm"} {
		f, ok := byCheck[checkID]
		if !ok {
			t.Errorf("check %s missing from report", checkID)
			continue
		}
		if f.Severity == OK {
			t.Errorf("%s reported OK on a broken project", checkID)
		}
		if f.FixID == "" {
			t.Errorf("%s has a problem but no fix id", checkID)
		}
	}

	// Docroot is fine (we created public/).
	if byCheck["doc_root"].Severity != OK {
		t.Errorf("doc_root should be OK: %+v", byCheck["doc_root"])
	}

	if !report.HasFailed() {
		t.Error("report with failures should HasFailed")
	}
}

func TestHealthyLaravelAllOK(t *testing.T) {
	target, dir := laravelTarget(t)
	// Fix everything up.
	os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_KEY=base64:0123456789abcdef0123456789abcdef0123456789abcdef=\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "vendor"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	// Simulate the storage link (a directory works for this test's Lstat).
	os.MkdirAll(filepath.Join(target.DocRoot, "storage"), 0o755)
	target.HasVendor = true
	target.HasNodeModules = true
	target.HostsEntryPresent = true
	target.FpmRunning = true
	target.PhpExe = filepath.Join(dir, "php.exe")
	os.WriteFile(target.PhpExe, []byte("stub"), 0o644)
	target.PhpSeries = "8.4"

	report := Check(target, Dependencies{})
	for _, f := range report.Findings {
		if f.Severity == Failed {
			t.Errorf("%s = %s (%s) on a healthy project", f.CheckID, f.Severity, f.Message)
		}
	}
	if report.HasFailed() {
		t.Error("healthy project reports failure")
	}
}

func TestVhostDriftDetection(t *testing.T) {
	target, _ := laravelTarget(t)
	target.VhostExpected = "server {\n  listen 80;\n}\n"
	target.VhostOnDisk = "server {\n  listen 443 ssl;\n}\n"

	report := Check(target, Dependencies{})
	for _, f := range report.Findings {
		if f.CheckID == "vhost" && f.Severity != Warning {
			t.Errorf("drift not detected: %+v", f)
		}
		if f.CheckID == "vhost" && f.FixID != "vhost" {
			t.Errorf("drift fix id wrong: %+v", f)
		}
	}
}

func TestNonLaravelSkipsLaravelChecks(t *testing.T) {
	target := Target{Name: "site", Path: t.TempDir(), Kind: "plain",
		DocRoot: t.TempDir(), HostsEntryPresent: true}
	report := Check(target, Dependencies{})
	byCheck := map[string]Finding{}
	for _, f := range report.Findings {
		byCheck[f.CheckID] = f
	}
	for _, checkID := range []string{"env_file", "app_key", "storage_link", "migrations"} {
		if byCheck[checkID].Severity != NotChecked {
			t.Errorf("%s should be NotChecked for plain: %+v", checkID, byCheck[checkID])
		}
	}
}

func TestAppKeyValidation(t *testing.T) {
	target, dir := laravelTarget(t)

	// Short key: warning.
	os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_KEY=short\n"), 0o644)
	report := Check(target, Dependencies{})
	for _, f := range report.Findings {
		if f.CheckID == "app_key" && f.Severity != Warning {
			t.Errorf("short key should warn: %+v", f)
		}
	}

	// Valid base64 key: OK.
	os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_KEY=base64:MTIzNDU2Nzg5MGFiY2RlZjAxMjM0NTY3ODkwYWJjZGVm\n"), 0o644)
	report = Check(target, Dependencies{})
	for _, f := range report.Findings {
		if f.CheckID == "app_key" && f.Severity != OK {
			t.Errorf("valid key should pass: %+v", f)
		}
	}
}
