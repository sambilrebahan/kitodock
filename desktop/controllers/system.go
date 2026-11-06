package controllers

import (
	"context"
	"io/fs"

	"kitodock/internal/clipboard"
	"kitodock/internal/config"
	"kitodock/internal/i18n"
	"kitodock/internal/version"
)

// System is the controller for app-level concerns: the build version, the UI
// language and the clipboard. Ports, metrics and updates join it in Phase 1+.
type System struct {
	cfg *config.Store
	bus *EventBus
}

func NewSystem(cfg *config.Store, bus *EventBus) *System {
	return &System{cfg: cfg, bus: bus}
}

// InstallCatalogues parses the embedded catalogues and installs them as the
// global bundle every backend string renders against. The composition root
// calls it before anything else can produce a message, so no code path ever
// runs with an uninstalled bundle outside of tests.
func (s *System) InstallCatalogues(fsys fs.FS) {
	bundle, err := i18n.Load(fsys, "langs")
	if err != nil {
		panic("load translation catalogues: " + err.Error())
	}
	i18n.SetGlobal(bundle)
	i18n.SetActive(s.currentLanguage())
}

// AppVersion is the build version, injected via -ldflags at release time.
func (s *System) AppVersion() string { return version.Version }

// currentLanguage resolves the effective language: the persisted choice, or
// the OS locale matched against what shipped when nothing was chosen yet.
func (s *System) currentLanguage() string {
	if code := s.cfg.Language(); code != "" {
		return code
	}
	bundle := i18n.Global()
	if bundle == nil {
		return i18n.FallbackCode
	}
	var codes []string
	for _, meta := range bundle.Locales() {
		codes = append(codes, meta.Code)
	}
	return bundle.Match(i18n.OSLocale())
}

// GetLanguage is the effective UI language.
func (s *System) GetLanguage() string { return s.currentLanguage() }

// ListLocales feeds the Settings language selector: one entry per catalogue.
func (s *System) ListLocales() []i18n.Meta {
	if bundle := i18n.Global(); bundle != nil {
		return bundle.Locales()
	}
	return nil
}

// SetLanguage persists the choice, switches backend rendering to it and
// tells this window. Other Kitodock processes learn of the change from the
// configuration file (see WatchLanguageFor), which is the one channel every
// process agrees on.
func (s *System) SetLanguage(code string) error {
	bundle := i18n.Global()
	if bundle == nil || !bundle.ValidLocale(code) {
		return i18n.Fail("errors.app.unknownLanguage", map[string]any{"code": code})
	}
	if err := s.cfg.SetLanguage(code); err != nil {
		return err
	}
	i18n.SetActive(code)
	s.bus.Emit(EvLanguageChanged, code)
	return nil
}

// ReadClipboardText returns the OS clipboard's text.
func (s *System) ReadClipboardText() (string, error) { return clipboard.ReadText() }

// WriteClipboardText replaces the OS clipboard's text.
func (s *System) WriteClipboardText(text string) error { return clipboard.WriteText(text) }

// WatchLanguageFor mirrors language changes written by other Kitodock
// processes into this window. It is a free function, not a System method, so
// the Wails generator cannot expose a call that blocks until ctx ends.
// Blocks until ctx is done; run it on its own goroutine.
func WatchLanguageFor(ctx context.Context, cfg *config.Store, bus *EventBus) {
	cfg.Watch(ctx, func(fresh config.Config) {
		code := fresh.Language
		if code == "" {
			code = i18n.Active()
		}
		i18n.SetActive(code)
		bus.Emit(EvLanguageChanged, code)
	})
}
