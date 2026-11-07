package main

// stack.go turns the generic services.Manager into Kitodock's actual stack:
// one Definition per real binary, each with its first-run setup and command
// line. Paths resolve through internal/paths lazily so an install arriving
// mid-run just starts working.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"kitodock/internal/paths"
	"kitodock/internal/runtimeinventory"
	"kitodock/internal/services"
)

const webGroup = "web" // nginx/caddy both bind :80

func buildStack() *services.Manager {
	m := services.New()
	m.Register(nginxDefinition())
	m.Register(caddyDefinition())
	m.Register(mysqlDefinition())
	m.Register(postgresDefinition())
	m.Register(redisDefinition())
	m.Register(mailpitDefinition())
	return m
}

func nginxDefinition() services.Definition {
	return services.Definition{
		Name:     "nginx",
		LabelKey: "services.names.nginx",
		Group:    webGroup,
		Exe:      nginxExePath,
		Version:  installedVersionFunc(nginxExePath),
	}
}

func caddyDefinition() services.Definition {
	return services.Definition{
		Name:     "caddy",
		LabelKey: "services.names.caddy",
		Group:    webGroup,
		Exe:      caddyExePath,
		Args: func() []string {
			return []string{"run", "--config", "Caddyfile", "--adapter", "caddyfile"}
		},
		NeedsSetup: func() bool {
			_, err := os.Stat(filepath.Join(dirOf(caddyExePath()), "Caddyfile"))
			return err != nil
		},
		Setup: func(ctx context.Context, log func(string)) error {
			dir := dirOf(caddyExePath())
			if err := os.MkdirAll(filepath.Join(dir, "sites"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte("import sites/*\n"), 0o644)
		},
		Version: installedVersionFunc(caddyExePath),
	}
}

func mysqlDefinition() services.Definition {
	// The exe lives in <root>\mysqlin; mysqld wants the basedir above it.
	root := func() string { return filepath.Dir(dirOf(mysqlExePath())) }
	return services.Definition{
		Name:     "mysql",
		LabelKey: "services.names.mysql",
		Exe:      mysqlExePath,
		Args: func() []string {
			return []string{"--console", "--basedir=" + root(), "--datadir=" + filepath.Join(root(), "data")}
		},
		NeedsSetup: func() bool {
			_, err := os.Stat(filepath.Join(root(), "data", "mysql"))
			return err != nil
		},
		Setup: func(ctx context.Context, log func(string)) error {
			return runSetupStep(ctx, log, "initialize MySQL data directory",
				mysqlExePath(),
				[]string{
					"--initialize-insecure",
					"--basedir=" + root(),
					"--datadir=" + filepath.Join(root(), "data"),
				},
				180*time.Second)
		},
		Version: installedVersionFunc(mysqlExePath),
	}
}

func postgresDefinition() services.Definition {
	return services.Definition{
		Name:     "postgres",
		LabelKey: "services.names.postgres",
		Exe: func() string {
			exe := filepath.Join(postgresRoot(), "bin", "postgres.exe")
			if _, err := os.Stat(exe); err != nil {
				return ""
			}
			return exe
		},
		Args: func() []string {
			return []string{"-D", filepath.Join(postgresRoot(), "data")}
		},
		NeedsSetup: func() bool {
			_, err := os.Stat(filepath.Join(postgresRoot(), "data", "PG_VERSION"))
			return err != nil
		},
		Setup: func(ctx context.Context, log func(string)) error {
			initdb := filepath.Join(postgresRoot(), "bin", "initdb.exe")
			if _, err := os.Stat(initdb); err != nil {
				return fmt.Errorf("initdb.exe missing from the postgres install")
			}
			return runSetupStep(ctx, log, "initialize PostgreSQL cluster", initdb,
				[]string{
					"-U", "postgres", "--auth=trust", "--encoding=UTF8",
					"-D", filepath.Join(postgresRoot(), "data"),
				},
				180*time.Second)
		},
		Version: installedVersionFunc(func() string {
			exe := filepath.Join(postgresRoot(), "bin", "postgres.exe")
			if _, err := os.Stat(exe); err != nil {
				return ""
			}
			return exe
		}),
	}
}

func redisDefinition() services.Definition {
	return services.Definition{
		Name:     "redis",
		LabelKey: "services.names.redis",
		Exe:      redisExePath,
		Args: func() []string {
			return []string{filepath.Join(dirOf(redisExePath()), "redis.windows.conf")}
		},
		NeedsSetup: func() bool {
			_, err := os.Stat(filepath.Join(dirOf(redisExePath()), "redis.windows.conf"))
			return err != nil
		},
		Setup: func(ctx context.Context, log func(string)) error {
			dir := dirOf(redisExePath())
			if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
				return err
			}
			conf := strings.Join([]string{
				"port 6379",
				"bind 127.0.0.1",
				`dir ./data`,
				"appendonly no",
			}, "\n") + "\n"
			return os.WriteFile(filepath.Join(dir, "redis.windows.conf"), []byte(conf), 0o644)
		},
		Version: installedVersionFunc(redisExePath),
	}
}

func mailpitDefinition() services.Definition {
	return services.Definition{
		Name:     "mailpit",
		LabelKey: "services.names.mailpit",
		Exe:      mailpitExePath,
		Version:  installedVersionFunc(mailpitExePath),
	}
}

// runSetupStep runs a first-run helper binary with its output streaming into
// the service log ring and a generous but real timeout.
func runSetupStep(ctx context.Context, log func(string), what string, exe string, args []string, timeout time.Duration) error {
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(stepCtx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	log(fmt.Sprintf("%s…", what))
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := pipe.Read(buf)
			if n > 0 {
				for _, line := range strings.Split(strings.TrimRight(string(buf[:n]), "\r\n"), "\n") {
					if line != "" {
						log(line)
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	log(what + " done")
	return nil
}

// installedVersionFunc reads a service binary's version lazily; "" while
// the service is absent, so the dashboard shows a version when it has one.
func installedVersionFunc(exe func() string) func() string {
	return func() string {
		path := exe()
		if path == "" {
			return ""
		}
		meta, err := runtimeinventory.Read(path)
		if err != nil {
			return ""
		}
		return meta.FullVersion
	}
}

// --- binary paths ---------------------------------------------------------
// Each resolver returns "" while its service is not installed; the manager
// turns that into a visible "not installed" state rather than an error.

func nginxExePath() string   { return exeIfPresent(pathsRootJoin("nginx", "nginx.exe")) }
func caddyExePath() string   { return exeIfPresent(pathsRootJoin("caddy", "caddy.exe")) }
func redisExePath() string   { return exeIfPresent(pathsRootJoin("redis", "redis-server.exe")) }
func mailpitExePath() string { return exeIfPresent(pathsRootJoin("mailpit", "mailpit.exe")) }
func mysqlExePath() string   { return exeIfPresent(pathsRootJoin("mysql", "bin", "mysqld.exe")) }

func postgresRoot() string { return pathsRootJoin("postgres") }

func pathsRootJoin(parts ...string) string {
	return filepath.Join(append([]string{paths.Root()}, parts...)...)
}

func exeIfPresent(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func dirOf(exe string) string { return filepath.Dir(exe) }
