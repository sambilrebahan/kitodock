package controllers

// Controller-level tests for the Phase 5 SSH + Deployments wiring that
// needs no live network: freeze snapshots, bundle import semantics,
// preset inventory, and secret redaction in previews. The event bus runs
// context-less, so emissions are no-ops.

import (
	"path/filepath"
	"strings"
	"testing"

	"kitodock/internal/credentials"
	"kitodock/internal/deploy"
	"kitodock/internal/deployments"
	"kitodock/internal/ssh"
)

// identitySealer passes secrets through unchanged for tests.
type identitySealer struct{}

func (identitySealer) Seal(plain string) (string, error) { return "id:" + plain, nil }
func (identitySealer) Unseal(sealed string) (string, error) {
	return strings.TrimPrefix(sealed, "id:"), nil
}

var _ credentials.Sealer = identitySealer{}

func newTestControllers(t *testing.T) (*SSH, *Deployments) {
	t.Helper()
	base := t.TempDir()
	sshCtrl := NewSSH(NewEventBus(), identitySealer{}, func() string {
		return filepath.Join(base, "ssh")
	})
	deployCtrl, err := NewDeployments(NewEventBus(),
		filepath.Join(base, "deployments", "index.json"), identitySealer{}, sshCtrl)
	if err != nil {
		t.Fatal(err)
	}
	return sshCtrl, deployCtrl
}

func TestSFTPFrozenSnapshot(t *testing.T) {
	sshCtrl, _ := newTestControllers(t)

	// Seed a registry entry with a nil client: the frozen+cached path
	// must never touch the client, so nil proves the snapshot is served.
	sshCtrl.mu.Lock()
	sshCtrl.sftp["f1"] = &sftpEntry{host: "prod"}
	sshCtrl.listingCache["prod|/var/www"] = []ssh.Entry{{Name: "before-deploy"}}
	sshCtrl.mu.Unlock()

	sshCtrl.SetHostFrozen("prod", true)
	entries, err := sshCtrl.SFTPList("f1", "/var/www")
	if err != nil || len(entries) != 1 || entries[0].Name != "before-deploy" {
		t.Fatalf("frozen listing = %v err=%v", entries, err)
	}

	// Uncached paths refuse instead of reading mid-deploy state.
	if _, err := sshCtrl.SFTPList("f1", "/etc"); err == nil {
		t.Fatal("uncached path served during freeze")
	}

	// Unfreezing clears the snapshot mode (the live-listing path itself
	// needs a real client, covered by the internal/ssh suite).
	sshCtrl.SetHostFrozen("prod", false)
	sshCtrl.mu.Lock()
	stillFrozen := sshCtrl.frozen["prod"]
	sshCtrl.mu.Unlock()
	if stillFrozen {
		t.Fatal("freeze flag survived unfreeze")
	}
}

func TestBundleImportHostConflictsAndIdempotency(t *testing.T) {
	sshCtrl, deployCtrl := newTestControllers(t)

	// A local host named "prod" on a different address than the bundle's.
	if err := sshCtrl.SaveHost(ssh.Host{
		Name: "prod", Address: "127.0.0.1", Port: 22, User: "root", AuthMode: "password",
	}, "local-pass"); err != nil {
		t.Fatal(err)
	}

	bundle := `{
	  "version": 1,
	  "hosts": [
	    {"name": "prod", "address": "203.0.113.99", "port": 22, "user": "root", "authMode": "key"},
	    {"name": "staging", "address": "203.0.113.50", "port": 2222, "user": "deploy", "authMode": "password"}
	  ],
	  "deployments": [
	    {"name": "shop", "hostName": "prod", "config": {"projectName": "shop", "domain": "shop.io"}},
	    {"name": "blog", "hostName": "staging", "config": {"projectName": "blog", "domain": "blog.io"}}
	  ]
	}`

	result, err := deployCtrl.ImportDeploymentBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if result.HostsAdded != 1 || result.HostsSkipped != 1 {
		t.Fatalf("host counters = %+v", result)
	}
	if result.DeploymentsAdded != 2 {
		t.Fatalf("deployment counters = %+v", result)
	}
	if result.HostConflicts["prod"] != "127.0.0.1" {
		t.Fatalf("conflict map = %v", result.HostConflicts)
	}

	// The conflicting host keeps its local address; the new one imported.
	hosts, _ := sshCtrl.ListHosts()
	byName := map[string]ssh.Host{}
	for _, h := range hosts {
		byName[h.Name] = h
	}
	if byName["prod"].Address != "127.0.0.1" {
		t.Fatalf("local host overwritten: %+v", byName["prod"])
	}
	if byName["staging"].Address != "203.0.113.50" || byName["staging"].Port != 2222 {
		t.Fatalf("staging not imported: %+v", byName["staging"])
	}
	if byName["staging"].Password != "" {
		t.Fatal("imported host carried a password")
	}

	// "shop" was remapped to the local prod host despite the bundle's
	// conflicting address.
	records := deployCtrl.ListDeployments()
	if len(records) != 2 {
		t.Fatalf("records = %d", len(records))
	}
	for _, rec := range records {
		view, err := deployCtrl.GetDeployment(rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Name == "shop" && view.HostName != "prod" {
			t.Fatalf("shop host remapped to %q", view.HostName)
		}
	}

	// Second import is a full no-op.
	result, err = deployCtrl.ImportDeploymentBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if result.HostsAdded != 0 || result.DeploymentsAdded != 0 ||
		result.HostsSkipped != 2 || result.DeploymentsSkipped != 2 {
		t.Fatalf("re-import = %+v", result)
	}
	if len(deployCtrl.ListDeployments()) != 2 {
		t.Fatal("re-import duplicated records")
	}

	// Version mismatch is refused.
	if _, err := deployCtrl.ImportDeploymentBundle(`{"version": 99}`); err == nil {
		t.Fatal("bad version accepted")
	}
}

func TestBuiltinPresets(t *testing.T) {
	_, deployCtrl := newTestControllers(t)
	presets := deployCtrl.DeployPresets()
	if len(presets) != 3 {
		t.Fatalf("presets = %d, want 3", len(presets))
	}
	seen := map[string]bool{}
	for _, p := range presets {
		seen[p.ID] = true
		if !p.BuiltIn || p.Script == "" {
			t.Fatalf("preset %s incomplete: %+v", p.ID, p)
		}
	}
	for _, want := range []string{"git-pull-update", "laravel-production", "static-site"} {
		if !seen[want] {
			t.Errorf("missing preset %s", want)
		}
	}
}

func TestPlanPreviewRedactsSecrets(t *testing.T) {
	_, deployCtrl := newTestControllers(t)
	view := DeploymentView{
		Name: "shop", HostName: "prod",
		Config: deploy.ApplyConfig{
			ProjectName: "shop", Domain: "shop.io", GitBranch: "main",
			GitURL: "https://github.com/u/shop.git", GitPAT: "ghp_previewsecret1",
		},
	}
	steps := deployCtrl.DeployPlanPreview(view)
	if len(steps) == 0 {
		t.Fatal("no preview steps")
	}
	for _, step := range steps {
		if strings.Contains(step.Command, "ghp_previewsecret1") {
			t.Fatalf("preview leaked PAT in step %q", step.Title)
		}
	}
	// The destroy preview is redacted too.
	destroy := deployCtrl.DeployDestroyPreview(view, false)
	for _, step := range destroy {
		if strings.Contains(step.Command, "ghp_previewsecret1") {
			t.Fatalf("destroy preview leaked PAT in step %q", step.Title)
		}
	}
}

func TestSaveDeploymentNormalizesDatabase(t *testing.T) {
	_, deployCtrl := newTestControllers(t)
	view := DeploymentView{
		Name: "shop", HostName: "prod",
		Config: deploy.ApplyConfig{
			ProjectName: "shop", Domain: "shop.io",
			Database: deploy.Database{Engine: "mysql", Name: "shop"},
		},
	}
	if err := deployCtrl.SaveDeployment(view); err != nil {
		t.Fatal(err)
	}
	loaded, err := deployCtrl.GetDeployment(deployCtrl.ListDeployments()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	db := loaded.Config.Database
	if db.Password == "" || db.AccessPort != deploy.DefaultMySQLPort || db.User != "root" {
		t.Fatalf("normalized db = %+v", db)
	}

	// Misconfiguration is refused before anything is stored.
	view.Name = "broken"
	view.Config.Database.Name = ""
	if err := deployCtrl.SaveDeployment(view); err == nil {
		t.Fatal("engine without name accepted")
	}
	if len(deployCtrl.ListDeployments()) != 1 {
		t.Fatal("failed save still stored a record")
	}
	_ = deployments.BundleVersion // keep the import honest
}
