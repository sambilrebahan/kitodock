package controllers

// The projects controller: the serving wiring behind the Projects screen —
// list with resolved runtimes, per-project overrides, SSL, TLD.

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"kitodock/internal/config"
	knode "kitodock/internal/nodejs"
	kphp "kitodock/internal/php"
	"kitodock/internal/projects"
)

// ProjectServing is what the controller needs from the app's serving
// wiring (implemented by desktop/serving.go; kept structural so the
// controllers package stays free of package-main).
type ProjectServing interface {
	ListProjects() []projects.Project
	ApplyProject(ctx context.Context, project projects.Project) error
	RemoveProject(project projects.Project) error
}

// Projects is the Projects screen's controller.
type Projects struct {
	bus     *EventBus
	cfg     *config.Store
	serving ProjectServing
	php     *kphp.Manager
	node    *knode.Manager
	ctx     context.Context
}

func NewProjects(bus *EventBus, cfg *config.Store, serving ProjectServing, php *kphp.Manager, node *knode.Manager) *Projects {
	return &Projects{bus: bus, cfg: cfg, serving: serving, php: php, node: node}
}

// SetContext installs the Wails context (URL opening).
func (p *Projects) SetContext(ctx context.Context) { p.ctx = ctx }

// ProjectView is one row of the Projects screen.
type ProjectView struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	SSL            bool   `json:"ssl"`
	URL            string `json:"url"`
	PhpSeries      string `json:"phpSeries,omitempty"`
	PhpSource      string `json:"phpSource,omitempty"`
	PhpProblem     string `json:"phpProblem,omitempty"`
	PhpInstallTip  string `json:"phpInstallTip,omitempty"`
	NodeVersion    string `json:"nodeVersion,omitempty"`
	JsFramework    string `json:"jsFramework,omitempty"`
	PackageManager string `json:"packageManager,omitempty"`
}

// ListProjects merges scan + preference + runtime resolution.
func (p *Projects) ListProjects() []ProjectView {
	prefs := p.cfg.Get()
	tld := prefs.Projects.TLD

	var phpCandidates, phpCatalog []projects.InstalledRuntime
	for _, v := range p.php.List() {
		phpCandidates = append(phpCandidates, projects.InstalledRuntime{Version: v.Series, FullVersion: v.FullVersion})
	}
	for _, pin := range []struct{ series, full string }{
		{"8.5", "8.5.12"}, {"8.4", "8.4.25"}, {"8.3", "8.3.36"},
	} {
		phpCatalog = append(phpCatalog, projects.InstalledRuntime{Version: pin.series, FullVersion: pin.full})
	}
	var nodeCandidates []projects.InstalledRuntime
	for _, v := range p.node.List() {
		nodeCandidates = append(nodeCandidates, projects.InstalledRuntime{Version: v.FullVersion, FullVersion: v.FullVersion})
	}

	out := []ProjectView{}
	for _, project := range p.serving.ListProjects() {
		view := ProjectView{
			Name:           project.Name,
			Kind:           string(project.Kind),
			SSL:            prefs.Projects.SSL[project.Name],
			URL:            "http://" + project.Name + "." + tld,
			JsFramework:    project.JSFramework,
			PackageManager: project.PackageManager,
		}
		if project.SSL {
			view.URL = "https://" + project.Name + "." + tld
		}

		// PHP: manual override wins (validated), else constraint chain.
		phpConstraint := projects.PhpConstraint(project.Path)
		if override := prefs.Projects.PhpVersions[project.Name]; override != "" {
			res := projects.CheckOverride(phpConstraint, false, override, phpCandidates)
			view.PhpSeries = override
			view.PhpSource = "manual"
			if res.Status == projects.StatusIncompatible {
				view.PhpProblem = res.Problem
			}
		} else if phpConstraint != "" {
			res := projects.Resolve(phpConstraint, "composer.json", false, phpCandidates, phpCatalog)
			view.PhpSource = "composer.json"
			view.applyPhp(res)
		} else if raw := readDotFile(project.Path, ".php-version"); raw != "" {
			res := projects.Resolve(raw, ".php-version", true, phpCandidates, phpCatalog)
			view.PhpSource = ".php-version"
			view.applyPhp(res)
		}

		// Node: same ladder.
		nodeConstraint := projects.NodeConstraint(project.Path)
		if override := prefs.Projects.NodeVersions[project.Name]; override != "" {
			res := projects.CheckOverride(nodeConstraint, false, override, nodeCandidates)
			view.NodeVersion = override
			if res.Status == projects.StatusIncompatible {
				view.PhpProblem += " · node: " + res.Problem // surfaced in one field for now
			}
		} else if raw := readDotFile(project.Path, ".nvmrc"); raw != "" {
			res := projects.Resolve(normalizeNvmrc(raw), ".nvmrc", true, nodeCandidates, nil)
			view.applyNode(res)
		} else if nodeConstraint != "" {
			res := projects.Resolve(nodeConstraint, "engines.node", false, nodeCandidates, nil)
			view.applyNode(res)
		}

		out = append(out, view)
	}
	return out
}

func (v *ProjectView) applyPhp(res projects.Resolution) {
	if res.Status == projects.StatusResolved {
		v.PhpSeries = res.Version
		return
	}
	if res.Status == projects.StatusMissing {
		v.PhpProblem = "constraint not satisfied by any installed PHP"
		v.PhpInstallTip = res.InstallVersion
	}
}

func (v *ProjectView) applyNode(res projects.Resolution) {
	if res.Status == projects.StatusResolved {
		v.NodeVersion = res.Version
	}
}

// SetProjectSSL toggles HTTPS (re-applies serving).
func (p *Projects) SetProjectSSL(name string, enabled bool) error {
	if err := p.cfg.SetProjectSSL(name, enabled); err != nil {
		return err
	}
	p.reapply(name)
	p.bus.Emit(EvProjectChanged, map[string]any{"kind": "ssl", "name": name})
	return nil
}

// SetProjectPhp pins a series; "" returns to auto detection.
func (p *Projects) SetProjectPhp(name, series string) error {
	if err := p.cfg.SetProjectPhp(name, series); err != nil {
		return err
	}
	p.reapply(name)
	p.bus.Emit(EvProjectChanged, map[string]any{"kind": "php", "name": name})
	return nil
}

// ListPhpSeries is the picker's data: installed series, newest first.
func (p *Projects) ListPhpSeries() []string {
	var out []string
	for _, v := range p.php.List() {
		out = append(out, v.Series)
	}
	return out
}

// SetProjectsTLD changes the TLD and migrates every project's serving.
func (p *Projects) SetProjectsTLD(tld string) error {
	tld = strings.TrimSpace(strings.Trim(strings.TrimSpace(tld), "."))
	if tld == "" {
		tld = "test"
	}
	// Remove under the old TLD, persist, re-apply under the new one.
	for _, project := range p.serving.ListProjects() {
		_ = p.serving.RemoveProject(project)
	}
	if err := p.cfg.SetProjectsTLD(tld); err != nil {
		return err
	}
	for _, project := range p.serving.ListProjects() {
		if err := p.serving.ApplyProject(context.Background(), project); err != nil {
			p.bus.Emit(EvProjectError, map[string]any{"name": project.Name, "message": err.Error()})
		}
	}
	p.bus.Emit(EvProjectChanged, map[string]any{"kind": "tld"})
	return nil
}

// GetTLD is the current suffix.
func (p *Projects) GetTLD() string { return p.cfg.Get().Projects.TLD }

// OpenProjectURL opens the project's URL in the browser.
func (p *Projects) OpenProjectURL(name string) error {
	tld := p.cfg.Get().Projects.TLD
	scheme := "http"
	if p.cfg.Get().Projects.SSL[name] {
		scheme = "https"
	}
	if p.ctx != nil {
		wruntime.BrowserOpenURL(p.ctx, scheme+"://"+name+"."+tld)
	}
	return nil
}

// Rescan re-applies one project's serving configuration.
func (p *Projects) Rescan(name string) {
	p.reapply(name)
}

func (p *Projects) reapply(name string) {
	for _, project := range p.serving.ListProjects() {
		if project.Name == name {
			if err := p.serving.ApplyProject(context.Background(), project); err != nil {
				p.bus.Emit(EvProjectError, map[string]any{"name": name, "message": err.Error()})
			}
			return
		}
	}
}

// LocalCAStatus surfaces the CA card's data for Settings.
func (p *Projects) LocalCAStatus() map[string]any {
	status := localCAStatus()
	return map[string]any{
		"exists":     status.Exists,
		"commonName": status.CommonName,
		"thumbprint": status.Thumbprint,
		"trusted":    status.Trusted,
	}
}

// TrustLocalCA imports the CA into the machine root store.
func (p *Projects) TrustLocalCA() error {
	return trustLocalCA()
}

func readDotFile(path, name string) string {
	data, err := os.ReadFile(filepath.Join(path, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// normalizeNvmrc strips the "v" prefix Node users write.
func normalizeNvmrc(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "v")
}

// CaStatus is the CA card's data (mirrors internal/vhost.CAStatus).
type CaStatus struct {
	Exists, Trusted        bool
	CommonName, Thumbprint string
}

// The CA hooks are set by the desktop layer once paths are known;
// nil-safe defaults keep the controller constructible in tests.
var (
	localCAStatus = func() CaStatus { return CaStatus{} }
	trustLocalCA  = func() error { return nil }
)

// SetLocalCA installs the status hook.
func SetLocalCA(read func() CaStatus) { localCAStatus = read }

// SetTrust installs the trust hook.
func SetTrust(trust func() error) { trustLocalCA = trust }
