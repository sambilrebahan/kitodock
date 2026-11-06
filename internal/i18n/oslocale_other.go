//go:build !windows

package i18n

// OSLocale returns nothing off Windows; first-run language matching simply
// falls back to English until a per-OS implementation ships.
func OSLocale() string { return "" }
