package installer

// Catalog returns the pinned installable set. Every URL below was verified
// by hand at pin time; refreshing a pin is a deliberate change to this file.
//
// PostgreSQL stays Manual: EnterpriseDB's download endpoint declines
// scripted fetches, so the user grabs the archive and InstallFromArchive
// takes over.

import (
	"kitodock/internal/paths"
	"kitodock/internal/vcruntime"
)

func phpReleaseURL(name string) string {
	return "https://windows.php.net/downloads/releases/" + name
}

func phpArchiveURL(name string) string {
	return "https://windows.php.net/downloads/releases/archives/" + name
}

func Catalog() []Source {
	sources := []Source{
		{
			Kind: KindWebServer, ID: "nginx", Version: "1.28.0",
			LabelKey:  "sources.names.nginx",
			URL:       "https://nginx.org/download/nginx-1.28.0.zip",
			Flatten:   true,
			InstallTo: paths.NginxDir,
			ExeName:   "nginx.exe",
		},
		{
			Kind: KindWebServer, ID: "caddy", Version: "2.10.2",
			LabelKey:  "sources.names.caddy",
			URL:       "https://github.com/caddyserver/caddy/releases/download/v2.10.2/caddy_2.10.2_windows_amd64.zip",
			InstallTo: paths.CaddyDir,
			ExeName:   "caddy.exe",
		},
		{
			Kind: KindDatabase, ID: "mysql", Version: "8.0.43",
			LabelKey:  "sources.names.mysql",
			URL:       "https://dev.mysql.com/get/Downloads/MySQL-8.0/mysql-8.0.43-winx64.zip",
			Flatten:   true,
			InstallTo: paths.MySQLDir,
			ExeName:   "bin/mysqld.exe",
		},
		{
			Kind: KindDatabase, ID: "postgres", Version: "17.6",
			LabelKey:  "sources.names.postgres",
			Manual:    true,
			PageURL:   "https://www.enterprisedb.com/downloads/postgres-postgresql-downloads",
			Flatten:   true,
			InstallTo: paths.PostgresDir,
			ExeName:   "bin/postgres.exe",
		},
		{
			Kind: KindCache, ID: "redis", Version: "5.0.14.1",
			LabelKey:  "sources.names.redis",
			URL:       "https://github.com/tporadowski/redis/releases/download/v5.0.14.1/Redis-x64-5.0.14.1.zip",
			InstallTo: paths.RedisDir,
			ExeName:   "redis-server.exe",
		},
		{
			Kind: KindMail, ID: "mailpit", Version: "1.33.0",
			LabelKey:  "sources.names.mailpit",
			URL:       "https://github.com/axllent/mailpit/releases/download/v1.33.0/mailpit-windows-amd64.zip",
			InstallTo: paths.MailpitDir,
			ExeName:   "mailpit.exe",
		},
		{
			Kind: KindTool, ID: "composer", Version: "2.8.9",
			LabelKey:  "sources.names.composer",
			URL:       "https://getcomposer.org/download/2.8.9/composer.phar",
			Direct:    true,
			InstallTo: func() string { return paths.Root() + "\\composer" },
		},
		{
			Kind: KindRuntime, ID: "php", Version: "8.5.12",
			LabelKey:       "sources.names.php85",
			URL:            phpReleaseURL("php-8.5.12-1-nts-Win32-vs17-x64.zip"),
			InstallTo:      func() string { return paths.PHPDir() + "\\8.5" },
			ExeName:        "php.exe",
			RuntimeVersion: false, // build suffix ("-1") differs from the pin; marker decides
			RedistURL:      vcruntime.VS17URL,
		},
		{
			Kind: KindRuntime, ID: "php", Version: "8.4.25",
			LabelKey:  "sources.names.php84",
			URL:       phpArchiveURL("php-8.4.25-nts-Win32-vs17-x64.zip"),
			InstallTo: func() string { return paths.PHPDir() + "\\8.4" },
			ExeName:   "php.exe",
			RedistURL: vcruntime.VS17URL,
		},
		{
			Kind: KindRuntime, ID: "php", Version: "8.3.36",
			LabelKey:  "sources.names.php83",
			URL:       phpArchiveURL("php-8.3.36-nts-Win32-vs16-x64.zip"),
			InstallTo: func() string { return paths.PHPDir() + "\\8.3" },
			ExeName:   "php.exe",
			RedistURL: vcruntime.VS17URL,
		},
		{
			Kind: KindRuntime, ID: "node", Version: "24.22.2",
			LabelKey:  "sources.names.node24",
			URL:       "https://nodejs.org/dist/v24.22.2/node-v24.22.2-win-x64.zip",
			Flatten:   true,
			InstallTo: func() string { return paths.NodeDir() + "\\24.22.2" },
			ExeName:   "node.exe",
		},
	}
	return sources
}

// Find returns the catalog entry with matching id and version, or the
// newest pinned version of an id when version is empty.
func Find(id, version string) (Source, bool) {
	best := Source{}
	found := false
	for _, src := range Catalog() {
		if src.ID != id {
			continue
		}
		if version == "" {
			if !found || compareVersions(src.Version, best.Version) > 0 {
				best, found = src, true
			}
			continue
		}
		if src.Version == version {
			return src, true
		}
	}
	return best, found
}

// compareVersions orders dotted versions numerically, field by field.
func compareVersions(a, b string) int {
	fields := func(s string) []int {
		out := []int{}
		num := 0
		digits := false
		for _, r := range s {
			if r >= '0' && r <= '9' {
				num = num*10 + int(r-'0')
				digits = true
				continue
			}
			if digits {
				out = append(out, num)
				num, digits = 0, false
			}
		}
		if digits {
			out = append(out, num)
		}
		return out
	}
	fa, fb := fields(a), fields(b)
	for i := 0; i < len(fa) || i < len(fb); i++ {
		x, y := 0, 0
		if i < len(fa) {
			x = fa[i]
		}
		if i < len(fb) {
			y = fb[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}
