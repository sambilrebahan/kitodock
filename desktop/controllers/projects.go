package controllers

// The projects controller: the serving wiring behind the Projects screen —
// list with resolved runtimes, per-project overrides, SSL, TLD.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"kitodock/internal/atomicfile"
	"kitodock/internal/config"
	knode "kitodock/internal/nodejs"
	"kitodock/internal/paths"
	kphp "kitodock/internal/php"
	"kitodock/internal/projectroutes"
	"kitodock/internal/projects"
	"kitodock/internal/webtuning"
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
	// create is the extended serving seam; set via SetCreateServing.
	create CreateServing
}

func NewProjects(bus *EventBus, cfg *config.Store, serving ProjectServing, php *kphp.Manager, node *knode.Manager) *Projects {
	return &Projects{bus: bus, cfg: cfg, serving: serving, php: php, node: node}
}

// CreateServing is the extra surface creation and the vhost editor need.
type CreateServing interface {
	// ProjectsRoot is the parent folder new projects are created in.
	ProjectsRoot() string
	// NginxSitesDir / ReloadWebServers serve the vhost editor.
	NginxSitesDir() string
	ReloadWebServers()
	// ListRoutes exposes the mirrored route table.
	ListRoutes() []projectroutes.Route
}

// SetCreateServing installs the extended serving seam (called by the
// composition root right after construction).
func (p *Projects) SetCreateServing(s CreateServing) { p.create = s }

// CreateProjectRequest describes one scaffolding request from the UI.
type CreateProjectRequest struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	GitURL         string `json:"gitUrl,omitempty"`
	PackageManager string `json:"packageManager,omitempty"`
}

// CreateKinds lists the scaffolding kinds in picker order.
func (p *Projects) CreateKinds() []string {
	out := make([]string, len(projects.AllCreateKinds))
	for i, k := range projects.AllCreateKinds {
		out[i] = string(k)
	}
	return out
}

// CreateProject scaffolds a new project in the managed root, streaming
// progress as project:create events, then applies its serving config so
// the site is live immediately.
func (p *Projects) CreateProject(req CreateProjectRequest) error {
	if req.Name == "" || p.create == nil {
		return fmt.Errorf("project name is required")
	}
	creator := &projects.Creator{
		PhpExeFor: func() string {
			if dir := p.php.ActivePath(); dir != "" {
				return dir
			}
			return "php"
		},
		ComposerFor: func() string {
			if cmd := findComposer(p.php.ActivePath()); cmd != "" {
				return cmd
			}
			return "composer"
		},
		NodeDirFor: func() string { return paths.NodeDir() },
	}
	emit := func(line string) {
		p.bus.Emit(EvProjectCreate, map[string]any{"name": req.Name, "line": line})
	}
	if err := creator.Create(context.Background(), projects.CreateOptions{
		Name:           req.Name,
		Kind:           projects.CreateKind(req.Kind),
		Dir:            p.create.ProjectsRoot(),
		GitURL:         req.GitURL,
		PackageManager: req.PackageManager,
	}, emit); err != nil {
		p.bus.Emit(EvProjectCreate, map[string]any{"name": req.Name, "error": err.Error()})
		return err
	}
	p.bus.Emit(EvProjectCreate, map[string]any{"name": req.Name, "done": true})
	return nil
}

// VhostFile is one rendered server config for the editor.
type VhostFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// GetProjectVhost returns the project's nginx config for hand editing.
// The name must belong to a known project: the file path is built from it.
func (p *Projects) GetProjectVhost(name string) (VhostFile, error) {
	if p.create == nil {
		return VhostFile{}, fmt.Errorf("serving unavailable")
	}
	if !p.knownProject(name) {
		return VhostFile{}, fmt.Errorf("unknown project %q", name)
	}
	path := filepath.Join(p.create.NginxSitesDir(), name+".conf")
	data, err := os.ReadFile(path)
	if err != nil {
		return VhostFile{}, fmt.Errorf("no vhost for %s yet — open the project first", name)
	}
	return VhostFile{Path: path, Content: string(data)}, nil
}

// SaveProjectVhost writes a hand-edited vhost and reloads the web server.
// Re-applying the project from its card regenerates the managed render.
func (p *Projects) SaveProjectVhost(name, content string) error {
	if p.create == nil {
		return fmt.Errorf("serving unavailable")
	}
	if !p.knownProject(name) {
		return fmt.Errorf("unknown project %q", name)
	}
	path := filepath.Join(p.create.NginxSitesDir(), name+".conf")
	if err := atomicfile.Write(path, []byte(content), 0o644); err != nil {
		return err
	}
	p.create.ReloadWebServers()
	return nil
}

// PhpExtensionView merges ini state with the live module list.
type PhpExtensionView struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Shipped bool   `json:"shipped"`
	// Loaded reflects the running interpreter (php -m); best effort.
	Loaded bool `json:"loaded"`
}

// ListPhpExtensions reports one series' extensions with live load state.
func (p *Projects) ListPhpExtensions(series string) ([]PhpExtensionView, error) {
	exts, err := p.php.Extensions(series)
	if err != nil {
		return nil, err
	}
	loaded := map[string]bool{}
	for _, name := range p.php.LoadedExtensions(series) {
		loaded[name] = true
	}
	out := make([]PhpExtensionView, 0, len(exts))
	for _, e := range exts {
		out = append(out, PhpExtensionView{
			Name: e.Name, Enabled: e.Enabled, Shipped: e.Shipped,
			Loaded: loaded[e.Name],
		})
	}
	return out, nil
}

// SetPhpExtension toggles one ini entry for a series.
func (p *Projects) SetPhpExtension(series, name string, enabled bool) error {
	return p.php.SetExtension(series, name, enabled)
}

// --- Tuning & PECL -----------------------------------------------------------

// TuningView is one server's tunable directives ("" = default).
type TuningView struct {
	Server string            `json:"server"`
	Values map[string]string `json:"values"`
	// Available lists the tunable keys for the UI form.
	Available []string `json:"available"`
}

// nginxTunableOrder drives the form field order.
var nginxTunableOrder = []string{
	"worker_processes", "worker_connections", "client_max_body_size",
	"keepalive_timeout", "sendfile",
}

// phpTunableOrder drives the form field order.
var phpTunableOrder = []string{
	"memory_limit", "upload_max_filesize", "post_max_size",
	"max_execution_time", "max_input_time", "opcache.enable",
}

// GetTuning reads nginx and active-PHP tunables.
func (p *Projects) GetTuning() []TuningView {
	out := []TuningView{
		{Server: "nginx", Available: nginxTunableOrder},
	}
	conf := filepath.Join(paths.NginxDir(), "conf", "nginx.conf")
	if values, err := webtuning.Tuning(conf); err == nil {
		out[0].Values = values
	} else {
		out[0].Values = map[string]string{}
	}

	series := p.php.ActiveSeries()
	phpView := TuningView{Server: "php", Available: phpTunableOrder, Values: map[string]string{}}
	if series != "" {
		phpView.Server = "php-" + series
		if values, err := p.php.Tuning(series); err == nil {
			phpView.Values = values
		}
	}
	return append(out, phpView)
}

// SetTuning writes one directive and reloads the web server.
func (p *Projects) SetTuning(server, directive, value string) error {
	switch {
	case server == "nginx":
		conf := filepath.Join(paths.NginxDir(), "conf", "nginx.conf")
		if err := webtuning.SetTuning(conf, directive, value); err != nil {
			return err
		}
		if p.create != nil {
			p.create.ReloadWebServers()
		}
		return nil
	case strings.HasPrefix(server, "php"):
		series := strings.TrimPrefix(server, "php")
		series = strings.TrimPrefix(series, "-")
		if series == "" {
			series = p.php.ActiveSeries()
		}
		if series == "" {
			return fmt.Errorf("no PHP version is active")
		}
		return p.php.SetTuning(series, directive, value)
	}
	return fmt.Errorf("unknown tuning target %q", server)
}

// PeclExtensions returns the installable catalog.
func (p *Projects) PeclExtensions() []kphp.PeclExtension {
	return kphp.PeclCatalog()
}

// InstallPeclExtension downloads and enables one PECL extension for a
// series, streaming progress as php:pecl events.
func (p *Projects) InstallPeclExtension(series, name string) (kphp.PeclResult, error) {
	// Only catalog names may reach the downloader: the name builds both
	// the URL and the destination DLL path.
	known := false
	for _, entry := range kphp.PeclCatalog() {
		if entry.Name == name {
			known = true
			break
		}
	}
	if !known {
		return kphp.PeclResult{}, fmt.Errorf("unknown PECL extension %q", name)
	}
	inst := kphp.NewPeclInstaller(p.php)
	result, err := inst.Install(context.Background(), series, name, func(step string) {
		p.bus.Emit(EvPhpPecl, map[string]any{"series": series, "extension": name, "step": step})
	})
	if err != nil {
		return kphp.PeclResult{}, err
	}
	return *result, nil
}

// knownProject reports whether name is a scanned project.
func (p *Projects) knownProject(name string) bool {
	if name == "" || name != filepath.Base(name) {
		return false
	}
	for _, project := range p.serving.ListProjects() {
		if project.Name == name {
			return true
		}
	}
	return false
}

// ListProjectRoutes exposes the mirrored route table (canonical anchors
// and, later, preview worktrees).
func (p *Projects) ListProjectRoutes() []projectroutes.Route {
	if p.create == nil {
		return nil
	}
	return p.create.ListRoutes()
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
