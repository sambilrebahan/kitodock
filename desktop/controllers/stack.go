package controllers

import (
	"context"
	"errors"
	"fmt"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"kitodock/internal/config"
	"kitodock/internal/i18n"
	"kitodock/internal/installer"
	"kitodock/internal/paths"
	"kitodock/internal/services"
	"kitodock/internal/vcruntime"
)

// serviceSourceIDs are catalog ids that provide a dashboard service, so
// their Downloads rows carry the show-on-dashboard toggle.
var serviceSourceIDs = map[string]bool{
	"nginx": true, "caddy": true, "mysql": true, "postgres": true,
	"redis": true, "mailpit": true,
}

// Installer is the Downloads screen's controller.
type Installer struct {
	bus     *EventBus
	manager *installer.Manager
	ctx     context.Context
}

func NewInstaller(bus *EventBus) *Installer {
	i := &Installer{bus: bus}
	i.manager = installer.NewManager(paths.DownloadsCache(), func(p installer.Progress) {
		bus.Emit(EvInstallProgress, p)
	})
	return i
}

// SetContext installs the Wails context (dialogs and the browser need it).
func (i *Installer) SetContext(ctx context.Context) { i.ctx = ctx }

// SourceView is one catalog row for the UI.
type SourceView struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	LabelKey  string `json:"labelKey"`
	Installed bool   `json:"installed"`
	Manual    bool   `json:"manual"`
	PageURL   string `json:"pageURL,omitempty"`
	Service   bool   `json:"service"`
}

// ListSources is the pinned catalog with install states attached.
func (i *Installer) ListSources() []SourceView {
	out := []SourceView{}
	for _, src := range installer.Catalog() {
		out = append(out, SourceView{
			ID:        src.ID,
			Version:   src.Version,
			Kind:      string(src.Kind),
			LabelKey:  src.LabelKey,
			Installed: i.manager.IsInstalled(src),
			Manual:    src.Manual,
			PageURL:   src.PageURL,
			Service:   serviceSourceIDs[src.ID],
		})
	}
	return out
}

// InstallSource installs a pinned source asynchronously: progress and
// completion arrive as events, because a slow line outlives a promise.
func (i *Installer) InstallSource(id, version string) error {
	src, ok := installer.Find(id, version)
	if !ok {
		return fmt.Errorf("no catalog entry %s %s", id, version)
	}
	go i.runInstall(src)
	return nil
}

// InstallFromArchive asks for an archive the user fetched by hand (manual
// sources) and installs from it.
func (i *Installer) InstallFromArchive(id, version string) error {
	src, ok := installer.Find(id, version)
	if !ok {
		return fmt.Errorf("no catalog entry %s %s", id, version)
	}
	if i.ctx == nil {
		return errors.New("not ready")
	}
	path, err := wruntime.OpenFileDialog(i.ctx, wruntime.OpenDialogOptions{
		Title: "Choose the downloaded archive",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Archives (*.zip)", Pattern: "*.zip"},
		},
	})
	if err != nil {
		return err
	}
	if path == "" {
		return nil // the user changed their mind; nothing to do
	}
	go i.runInstallFrom(src, path)
	return nil
}

// OpenSourcePage opens a manual source's download page in the browser.
func (i *Installer) OpenSourcePage(id string) error {
	for _, src := range installer.Catalog() {
		if src.ID == id && src.PageURL != "" {
			if i.ctx != nil {
				wruntime.BrowserOpenURL(i.ctx, src.PageURL)
			}
			return nil
		}
	}
	return fmt.Errorf("no download page for %s", id)
}

func (i *Installer) runInstall(src installer.Source) {
	if err := i.manager.Install(context.Background(), src); err != nil {
		i.emitDone(src, err)
		return
	}
	i.emitDone(src, nil)
}

func (i *Installer) runInstallFrom(src installer.Source, archive string) {
	i.emitDone(src, i.manager.InstallFromArchive(context.Background(), src, archive))
}

func (i *Installer) emitDone(src installer.Source, err error) {
	done := map[string]any{"id": src.ID, "version": src.Version}
	if err == nil {
		done["ok"] = true
	} else {
		done["error"] = err.Error()
		var manual *installer.ErrManualNeeded
		if errors.As(err, &manual) {
			done["manual"] = true
			done["pageURL"] = manual.Source.PageURL
		}
	}
	i.bus.Emit(EvInstallDone, done)
}

// Services is the dashboard's controller: run the stack, read logs, decide
// what appears on the dashboard at all.
type Services struct {
	bus         *EventBus
	cfg         *config.Store
	mgr         *services.Manager
	install     *Installer
	recommended []string
	ctx         context.Context
}

func NewServices(bus *EventBus, cfg *config.Store, mgr *services.Manager, inst *Installer, recommended []string) *Services {
	s := &Services{bus: bus, cfg: cfg, mgr: mgr, install: inst, recommended: recommended}
	mgr.Notify(
		func(services.Status) { bus.Emit(EvServiceStatus, mgr.Snapshot()) },
		func(name string, err error) { s.reportFailure(name, err) },
	)
	return s
}

// SetContext installs the Wails context.
func (s *Services) SetContext(ctx context.Context) { s.ctx = ctx }

// reportFailure turns a service death into an event, attaching the
// redistributable download when that is the verdict.
func (s *Services) reportFailure(name string, err error) {
	payload := map[string]any{"name": name, "message": err.Error()}
	var missing *vcruntime.Missing
	if errors.As(err, &missing) {
		payload["redist"] = missing.URL
	}
	s.bus.Emit(EvServiceError, payload)
}

// ServiceView is one dashboard card.
type ServiceView struct {
	Name      string `json:"name"`
	LabelKey  string `json:"labelKey"`
	Running   bool   `json:"running"`
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	HasLogs   bool   `json:"hasLogs"`
}

// ListServices is the snapshot restricted to dashboard-visible services.
func (s *Services) ListServices() []ServiceView {
	visible := s.cfg.Get().Stack.Enabled
	out := []ServiceView{}
	for _, st := range s.mgr.Snapshot() {
		if !visible[st.Name] {
			continue
		}
		out = append(out, ServiceView{
			Name:      st.Name,
			LabelKey:  st.LabelKey,
			Running:   st.Running,
			Installed: st.Installed,
			Version:   st.Version,
			HasLogs:   len(s.mgr.Logs(st.Name)) > 0,
		})
	}
	return out
}

// StartService brings one service (and its dependencies) up.
func (s *Services) StartService(name string) error {
	err := s.mgr.Start(context.Background(), name)
	s.bus.Emit(EvServiceStatus, s.mgr.Snapshot())
	return s.explain(err)
}

// StopService ends one service (and its now-unneeded dependencies).
func (s *Services) StopService(name string) error {
	err := s.mgr.Stop(name)
	s.bus.Emit(EvServiceStatus, s.mgr.Snapshot())
	return err
}

// StartVisible starts every installed, dashboard-visible service. A web
// sibling already holding :80 is not an error while starting everything —
// anything else is reported once.
func (s *Services) StartVisible() error {
	var first error
	for _, name := range s.mgr.Names() {
		if !s.cfg.Get().Stack.Enabled[name] {
			continue
		}
		if err := s.mgr.Start(context.Background(), name); err != nil && first == nil {
			var clash *services.GroupConflictError
			if !errors.As(err, &clash) {
				first = s.explain(err)
			}
		}
	}
	s.bus.Emit(EvServiceStatus, s.mgr.Snapshot())
	return first
}

// StopVisible stops everything this manager runs.
func (s *Services) StopVisible() error {
	s.mgr.StopAll()
	s.bus.Emit(EvServiceStatus, s.mgr.Snapshot())
	return nil
}

// GetServiceLogs returns a service's output tail.
func (s *Services) GetServiceLogs(name string) []string { return s.mgr.Logs(name) }

// GetEnabledServices is the dashboard-visibility map.
func (s *Services) GetEnabledServices() map[string]bool {
	return s.cfg.Get().Stack.Enabled
}

// SetServiceEnabled toggles dashboard visibility for one service.
func (s *Services) SetServiceEnabled(name string, visible bool) error {
	if err := s.cfg.SetServiceEnabled(name, visible); err != nil {
		return err
	}
	s.bus.Emit(EvStackChanged, s.cfg.Get().Stack.Enabled)
	s.bus.Emit(EvServiceStatus, s.mgr.Snapshot())
	return nil
}

// RecommendedIDs lists the starter stack ids.
func (s *Services) RecommendedIDs() []string {
	return append([]string(nil), s.recommended...)
}

// IsRecommendedReady reports whether the starter stack is fully installed.
func (s *Services) IsRecommendedReady() bool {
	for _, id := range s.recommended {
		if src, ok := installer.Find(id, ""); !ok || !s.install.manager.IsInstalled(src) {
			return false
		}
	}
	return true
}

// BootstrapRecommended installs the starter stack asynchronously, service
// by service, then starts what succeeded. Everything arrives as events.
func (s *Services) BootstrapRecommended() error {
	ids := s.RecommendedIDs()
	go func() {
		for _, id := range ids {
			src, ok := installer.Find(id, "")
			if !ok {
				continue
			}
			if err := s.install.manager.Install(context.Background(), src); err != nil {
				s.install.emitDone(src, err)
				continue
			}
			s.install.emitDone(src, nil)
		}
		for _, id := range ids {
			_ = s.mgr.Start(context.Background(), id)
		}
		s.bus.Emit(EvServiceStatus, s.mgr.Snapshot())
	}()
	return nil
}

// explain maps the typed supervisor errors to localized text; the
// redistributable verdict keeps its exact English phrase on purpose (the
// UI matches on it).
func (s *Services) explain(err error) error {
	if err == nil {
		return nil
	}
	var clash *services.GroupConflictError
	if errors.As(err, &clash) {
		return i18n.Fail("errors.services.groupConflict",
			map[string]any{"other": clash.Blocking})
	}
	var missing *services.NotInstalledError
	if errors.As(err, &missing) {
		return i18n.Fail("errors.services.notInstalled",
			map[string]any{"name": missing.Name})
	}
	return err
}
