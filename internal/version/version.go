// Package version carries the build version. Release builds overwrite it
// with -ldflags "-X kitodock/internal/version.Version=x.y.z"; anything else
// keeps the dev marker.
package version

var Version = "0.0.0-dev"
