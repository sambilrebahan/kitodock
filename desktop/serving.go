package main

// serving.go connects projects to the web servers: registering php-cgi
// pools as internal services per installed PHP version, applying and
// removing vhosts (all three servers at once), hosts entries and
// certificates, and the startup sweep that self-heals everything after a
// move or an interrupted run.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kitodock/desktop/controllers"
	"kitodock/internal/atomicfile"
	"kitodock/internal/config"
	kphp "kitodock/internal/php"
	"kitodock/internal/projects"
	"kitodock/internal/services"
	"kitodock/internal/vhost"
)

// poolWorkers is the php-cgi pool size per version (each worker serves one
// request at a time).
const poolWorkers = 4

// phpManager owns the php/ tree; the serving wiring reads it.
type serving struct {
	cfg   *config.Store
	stack *services.Manager
	php   *kphp.Manager
	hosts vhost.Hosts
	emit  func(kind, detail string, err error)
}

func newServing(cfg *config.Store, stack *services.Manager, phpMgr *kphp.Manager, hostsFile string) *serving {
	return &serving{
		cfg:   cfg,
		stack: stack,
		php:   phpMgr,
		hosts: vhost.Hosts{Path: hostsFile},
		emit:  func(string, string, error) {},
	}
}

// Notify installs the event sink (project:* events).
func (s *serving) Notify(emit func(kind, detail string, err error)) { s.emit = emit }

// fpmServiceName is the supervisor id of a series' pool ("php-fpm-8.4").
func fpmServiceName(series string) string { return "php-fpm-" + series }

// RegisterPhpPools registers (or refreshes) one internal service per
// installed PHP version. Called at startup and after every PHP install.
func (s *serving) RegisterPhpPools() {
	installed := s.php.List()
	names := map[string]bool{}
	for _, version := range installed {
		series := version.Series
		names[fpmServiceName(series)] = true
		cgi := kphp.CgiExe(version.Dir)
		_ = s.php.EnsureIni(version.Dir)
		s.stack.Register(services.Definition{
			Name:     fpmServiceName(series),
			LabelKey: "services.names.phpFpm",
			Exe:      func() string { return cgi },
			// php-cgi -b binds each worker to its own port; the pool is
			// exposed to nginx as one upstream.
			Replicas: poolWorkers,
			ReplicaArgs: func(index int) []string {
				return []string{"-b", fmt.Sprintf("127.0.0.1:%d", kphp.ReplicaPort(series, index))}
			},
			Internal: true, // a dead pool restarts itself; the user never babysits it
		})
	}
	s.syncUpstreams(installed)
}

// syncUpstreams rewrites nginx's managed upstream block to match the
// installed versions and their pool ports.
func (s *serving) syncUpstreams(installed []kphp.Installed) {
	conf := filepath.Join(kitodockNginxConfDir(), "nginx.conf")
	if _, err := os.Stat(conf); err != nil {
		return // nginx not installed yet; the sweep covers it later
	}
	var upstreams []vhost.Upstream
	for _, version := range installed {
		ports := make([]int, poolWorkers)
		for i := range ports {
			ports[i] = kphp.ReplicaPort(version.Series, i)
		}
		upstreams = append(upstreams, vhost.Upstream{Name: kphp.UpstreamName(version.Series), Ports: ports})
	}
	if err := vhost.EnsurePhpUpstreams(conf, upstreams); err != nil {
		s.emit("project:error", "nginx", err)
	}
}

// ensureFpmRunning starts a series' pool (and its ini) when a PHP project
// is being served.
func (s *serving) ensureFpmRunning(ctx context.Context, series string) error {
	if series == "" {
		return nil
	}
	s.RegisterPhpPools()
	if running := false; !running {
		if err := s.stack.Start(ctx, fpmServiceName(series)); err != nil {
			return err
		}
	}
	return nil
}

// ApplyProject writes the full serving configuration for a project: vhost
// files for all three servers, hosts entry, certificates when SSL, the
// PHP pool when needed, and a reload of whichever web server runs.
func (s *serving) ApplyProject(ctx context.Context, project projects.Project) error {
	tld := s.cfg.Get().Projects.TLD
	hostname := project.Name + "." + tld

	detected := projects.Detect(project.Path)
	phpSeries := s.resolvePhpSeries(project)

	phpRoot := kitodockPHPDir()
	certsDir := filepath.Join(kitodockNginxDir(), "certs")

	spec := vhost.Spec{
		Folder:     project.Name,
		ServerName: hostname,
		DocRoot:    detected.DocRoot,
	}
	switch {
	case phpSeries != "":
		spec.Kind = vhost.KindPHP
		spec.FastCGIUpstream = kphp.UpstreamName(phpSeries)
		spec.FastCGIPort = kphp.PortFor(phpSeries)
	case detected.PackageManager != "" || detected.JSFramework != "":
		// JS-only projects serve through their dev server (proxy mode,
		// wired with the devserver in Phase 2b); until then, static.
		spec.Kind = vhost.KindStatic
	default:
		spec.Kind = vhost.KindStatic
	}
	_ = phpRoot

	if project.SSL {
		crt, key, err := vhost.EnsureLeafCert(certsDir, hostname)
		if err != nil {
			return fmt.Errorf("certificate for %s: %w", hostname, err)
		}
		spec.SSL = true
		spec.CertFile = crt
		spec.KeyFile = key
	}

	nginxSites := filepath.Join(kitodockNginxConfDir(), "sites")
	apacheSites := filepath.Join(kitodockApacheConfDir(), "sites")
	caddySites := filepath.Join(kitodockCaddyDir(), "sites")
	for _, dir := range []string{nginxSites, apacheSites, caddySites} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	nginxPath, apachePath, caddyPath := spec.Paths(nginxSites, apacheSites, caddySites)
	writes := []struct {
		path string
		body string
	}{
		{nginxPath, vhost.RenderNginx(spec)},
		{apachePath, vhost.RenderApache(spec)},
		{caddyPath, vhost.RenderCaddy(spec)},
	}
	for _, write := range writes {
		if err := atomicfile.Write(write.path, []byte(write.body), 0o644); err != nil {
			rollbackWrites(writes)
			return err
		}
	}

	if err := s.hosts.AddEntry(hostname); err != nil {
		// hosts is elevation-sensitive; report, keep the files.
		s.emit("project:error", project.Name, err)
	}

	if spec.Kind == vhost.KindPHP {
		if err := s.ensureFpmRunning(ctx, phpSeries); err != nil {
			return err
		}
	}

	s.reloadWebServers()
	s.emit("project:applied", project.Name, nil)
	return nil
}

// RemoveProject drops every serving artifact of a project.
func (s *serving) RemoveProject(project projects.Project) error {
	tld := s.cfg.Get().Projects.TLD
	hostname := project.Name + "." + tld

	nginxSites := filepath.Join(kitodockNginxConfDir(), "sites")
	apacheSites := filepath.Join(kitodockApacheConfDir(), "sites")
	caddySites := filepath.Join(kitodockCaddyDir(), "sites")
	removed := 0
	for _, path := range []string{
		filepath.Join(nginxSites, project.Name+".conf"),
		filepath.Join(apacheSites, project.Name+".conf"),
		filepath.Join(caddySites, project.Name+".caddy"),
	} {
		if err := os.Remove(path); err == nil {
			removed++
		}
	}
	if err := s.hosts.RemoveEntry(hostname); err != nil {
		s.emit("project:error", project.Name, err)
	}
	if removed > 0 {
		s.reloadWebServers()
	}
	s.emit("project:removed", project.Name, nil)
	return nil
}

// Sweep applies every current project — the startup self-heal after a
// move, a restore, or a partial failure.
func (s *serving) Sweep(ctx context.Context) {
	s.RegisterPhpPools()
	for _, project := range s.ListProjects() {
		if err := s.ApplyProject(ctx, project); err != nil {
			s.emit("project:error", project.Name, err)
		}
	}
}

// ListProjects scans the projects root merged with saved preferences.
func (s *serving) ListProjects() []projects.Project {
	prefs := s.cfg.Get()
	return projects.Scan(prefs.Projects.Root, projects.Preferences{SSL: prefs.Projects.SSL})
}

// resolvePhpSeries picks the PHP series a project serves with: manual
// override, then constraint resolution. Empty means "not a PHP project".
func (s *serving) resolvePhpSeries(project projects.Project) string {
	override := s.cfg.Get().Projects.PhpVersions[project.Name]
	if override != "" {
		return override
	}
	return resolveRuntime(project.Path, s.php)
}

// reloadWebServers bounces whichever server is running so config changes
// land. Cheap when nothing runs.
func (s *serving) reloadWebServers() {
	for _, name := range []string{"nginx", "caddy"} {
		for _, st := range s.stack.Snapshot() {
			if st.Name == name && st.Running {
				_ = s.stack.Restart(context.Background(), name)
			}
		}
	}
}

func rollbackWrites(writes []struct {
	path string
	body string
}) {
	for _, write := range writes {
		os.Remove(write.path)
	}
}

// resolveRuntime is the standalone constraint resolution for PHP (source
// order: .php-version, then composer.json).
func resolveRuntime(path string, mgr *kphp.Manager) string {
	var candidates []projects.InstalledRuntime
	for _, v := range mgr.List() {
		candidates = append(candidates, projects.InstalledRuntime{Version: v.Series, FullVersion: v.FullVersion})
	}
	var catalog []projects.InstalledRuntime
	for _, src := range installerCatalogPhp() {
		catalog = append(catalog, src)
	}

	if raw, err := os.ReadFile(filepath.Join(path, ".php-version")); err == nil {
		text := strings.TrimSpace(string(raw))
		if text != "" {
			res := projects.Resolve(text, ".php-version", true, candidates, catalog)
			if res.Status == projects.StatusResolved {
				return res.Version
			}
		}
	}
	if c := projects.PhpConstraint(path); c != "" {
		res := projects.Resolve(c, "composer.json", false, candidates, catalog)
		if res.Status == projects.StatusResolved {
			return res.Version
		}
	}
	return ""
}

// installerCatalogPhp mirrors the installer's PHP pins as install
// candidates for resolution suggestions (kept structural to avoid an
// import cycle with the desktop layer).
func installerCatalogPhp() []projects.InstalledRuntime {
	return []projects.InstalledRuntime{
		{Version: "8.5", FullVersion: "8.5.12"},
		{Version: "8.4", FullVersion: "8.4.25"},
		{Version: "8.3", FullVersion: "8.3.36"},
	}
}

var _ controllers.ProjectServing = (*serving)(nil)

// WireLocalCA connects the projects controller's CA hooks to the real
// implementation (called from NewApp after paths are known).
func WireLocalCA() {
	controllers.SetLocalCA(func() controllers.CaStatus {
		dir := filepath.Join(kitodockNginxDir(), "certs")
		st := vhost.LocalCAStatus(dir)
		return controllers.CaStatus{Exists: st.Exists, Trusted: st.Trusted, CommonName: st.CommonName, Thumbprint: st.Thumbprint}
	})
	controllers.SetTrust(func() error {
		return vhost.TrustRootCA(filepath.Join(kitodockNginxDir(), "certs"))
	})
}
