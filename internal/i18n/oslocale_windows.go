//go:build windows

package i18n

import (
	"golang.org/x/sys/windows/registry"
)

// OSLocale reports the user's UI locale as a BCP-47 tag ("id-ID", "en-US"),
// or "" when it cannot be read. The user's chosen display language lives in
// the Control Panel International key as a ready-made locale name, which is
// exactly the spelling the catalogue codes use.
func OSLocale() string {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Control Panel\International`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue("LocaleName")
	if err != nil {
		return ""
	}
	return value
}
