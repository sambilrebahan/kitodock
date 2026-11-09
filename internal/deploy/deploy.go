// Package deploy plans and executes remote-server deployment via SSH.
// The engine builds bash steps on the Go side, then runs them over an
// injected SSH connection, streaming output line-by-line.
package deploy

import (
	"fmt"
	"strings"
)

// ApplyConfig is the stored spec for what the server should look like.
type ApplyConfig struct {
	ProjectName string `json:"projectName"`
	Kind        string `json:"kind"` // "laravel" | "static"
	Domain      string `json:"domain"`
	Environment string `json:"environment"` // "production" | "staging" | "development"

	GitURL    string `json:"gitUrl"`
	GitBranch string `json:"gitBranch"`
	GitPAT    string `json:"gitPat,omitempty"` // spliced into the HTTPS clone URL

	WebServer  string `json:"webServer"`  // "nginx"
	PHPVersion string `json:"phpVersion"` // "8.3"

	// Database is the normalized database block; see NormalizeDatabaseAccess.
	Database Database `json:"database"`

	SSL struct {
		Enabled bool   `json:"enabled"`
		Email   string `json:"email"`
	} `json:"ssl"`

	AutoDeployCron string `json:"autoDeployCron"` // "*/5 * * * *"
}

// Database describes the remote database a deployment provisions. Access*
// fields say where the application reaches it; User/Password are
// provisioned on the server by the plan when empty.
type Database struct {
	// Engine: "mysql" | "postgres" | "" (no database).
	Engine string `json:"engine"`
	Name   string `json:"name"`
	// User is the database user the app connects as.
	User string `json:"user,omitempty"`
	// Password is provisioned when empty (generated, stored sealed with
	// the config). Never surfaced: the Redactor masks it.
	Password string `json:"password,omitempty"`
	// AccessHost/AccessPort default to 127.0.0.1 and the engine port.
	AccessHost string `json:"accessHost,omitempty"`
	AccessPort int    `json:"accessPort,omitempty"`
}

// EffectiveDomain prefixes the environment subdomain for non-production.
func (c ApplyConfig) EffectiveDomain() string {
	switch c.Environment {
	case "staging":
		return "staging." + c.Domain
	case "development":
		return "dev." + c.Domain
	default:
		return c.Domain
	}
}

// SpliceGitPAT injects the PAT into the HTTPS clone URL.
func SpliceGitPAT(url, pat string) string {
	if pat == "" || !strings.HasPrefix(url, "https://") {
		return url
	}
	// https://github.com/user/repo.git → https://x-access-token: PAT@github.com/user/repo.git
	return strings.Replace(url, "https://", "https://"+pat+"@", 1)
}

// Step is one bash command in the deployment plan.
type Step struct {
	Title   string `json:"title"`
	Sudo    bool   `json:"sudo"`
	Ignore  bool   `json:"ignore"` // failures tolerated
	Command string `json:"command"`
}

// Detection is the result of probing a fresh server.
type Detection struct {
	OS          string `json:"os"`
	OSVersion   string `json:"osVersion"`
	HasNginx    bool   `json:"hasNginx"`
	HasPHP      bool   `json:"hasPHP"`
	PHPVersion  string `json:"phpVersion"`
	HasGit      bool   `json:"hasGit"`
	HasMySQL    bool   `json:"hasMySQL"`
	HasComposer bool   `json:"hasComposer"`
	HasCertbot  bool   `json:"hasCertbot"`
	IsFresh     bool   `json:"isFresh"` // nothing installed → clean VPS
}

// Detector runs the ~24 detection probes over an SSH runner.
type Runner interface {
	Run(command string, sudo bool) (string, error)
}

// RunDetection probes the remote server's current state.
func RunDetection(run Runner) (*Detection, error) {
	d := &Detection{}

	if out, err := run.Run("cat /etc/os-release | grep '^ID=' | cut -d= -f2", false); err == nil {
		d.OS = strings.TrimSpace(strings.Trim(out, `"'
`))
	}
	if out, err := run.Run("cat /etc/os-release | grep '^VERSION_ID=' | cut -d= -f2", false); err == nil {
		d.OSVersion = strings.TrimSpace(strings.Trim(out, `"'
`))
	}
	if out, err := run.Run("which nginx", false); err == nil && strings.TrimSpace(out) != "" {
		d.HasNginx = true
	}
	if out, err := run.Run("which php", false); err == nil && strings.TrimSpace(out) != "" {
		d.HasPHP = true
		if ver, err := run.Run("php -r 'echo PHP_MAJOR_VERSION.\".\".PHP_MINOR_VERSION;'", false); err == nil {
			d.PHPVersion = strings.TrimSpace(ver)
		}
	}
	if out, err := run.Run("which git", false); err == nil && strings.TrimSpace(out) != "" {
		d.HasGit = true
	}
	if out, err := run.Run("which mysql", false); err == nil && strings.TrimSpace(out) != "" {
		d.HasMySQL = true
	}
	if out, err := run.Run("which composer", false); err == nil && strings.TrimSpace(out) != "" {
		d.HasComposer = true
	}
	if out, err := run.Run("which certbot", false); err == nil && strings.TrimSpace(out) != "" {
		d.HasCertbot = true
	}

	// A "fresh" server has nothing installed.
	d.IsFresh = !d.HasNginx && !d.HasPHP && !d.HasMySQL && !d.HasCertbot
	return d, nil
}

// BuildPlan generates the ordered bash steps to bring the server to the
// ApplyConfig's desired state. The database block is normalized first so
// generated passwords and default ports are part of the plan.
func BuildPlan(cfg ApplyConfig) []Step {
	var steps []Step
	add := func(title, cmd string, sudo, ignore bool) {
		steps = append(steps, Step{Title: title, Command: cmd, Sudo: sudo, Ignore: ignore})
	}

	db, dbErr := NormalizeDatabaseAccess(cfg.Database)
	if dbErr != nil {
		// Surface the misconfiguration as the first failing step rather
		// than refusing to build: the user sees exactly what to fix.
		return []Step{{Title: "Validate database configuration", Command: "false", Sudo: false, Ignore: false}}
	}
	cfg.Database = db

	domain := cfg.EffectiveDomain()
	deployRoot := "/var/www/" + cfg.ProjectName

	// System packages.
	add("Update package list", "apt-get update", true, false)
	add("Install base packages", "apt-get install -y software-properties-common curl git unzip", true, false)

	// PHP via sury.org (needed for PHP 8.x on Ubuntu).
	add("Add sury PHP repository",
		`add-apt-repository -y ppa:ondrej/php`, true, false)

	php := cfg.PHPVersion
	if php == "" {
		php = "8.3"
	}

	// Install PHP + extensions.
	phpPkgs := strings.Join([]string{
		"php" + php, "php" + php + "-fpm", "php" + php + "-cli",
		"php" + php + "-mbstring", "php" + php + "-xml", "php" + php + "-curl",
		"php" + php + "-mysql", "php" + php + "-zip", "php" + php + "-gd",
		"php" + php + "-bcmath",
	}, " ")
	if cfg.Database.Engine == "postgres" {
		phpPkgs += fmt.Sprintf(" php%s-pgsql", php)
	}
	add("Install PHP "+php, "apt-get install -y "+phpPkgs, true, false)

	// Web server.
	add("Install nginx", "apt-get install -y nginx", true, false)

	// Composer.
	add("Install Composer",
		`curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer`, true, false)

	// Deploy directory with preservation of .env and storage/.
	add("Create deployment directory", "mkdir -p "+deployRoot, true, false)
	add("Clone repository",
		fmt.Sprintf(`if [ ! -d %s/.git ]; then
  git clone --branch %s --single-branch %s %s
else
  cd %s && git fetch origin %s && git reset --hard origin/%s
fi`,
			deployRoot, cfg.GitBranch, SpliceGitPAT(cfg.GitURL, cfg.GitPAT), deployRoot,
			deployRoot, cfg.GitBranch, cfg.GitBranch),
		true, false)

	// Composer install.
	add("Install dependencies",
		fmt.Sprintf("cd %s && composer install --no-dev --optimize-autoloader --no-interaction", deployRoot),
		false, false)

	// Database provisioning (embedded scripts).
	if db.Engine != "" {
		add("Create database and grants", databaseCreateScript(db, deployRoot), true, false)
		add("Write database environment",
			fmt.Sprintf("cd %s && touch .env && grep -q '^DB_CONNECTION=' .env || cat >> .env <<'ENV'\n%s\nENV",
				deployRoot, databaseEnvSnippet(db)),
			false, false)
	}

	// Laravel setup.
	if cfg.Kind == "laravel" {
		add("Generate application key",
			fmt.Sprintf("cd %s && [ -f .env ] || cp .env.example .env; php artisan key:generate --force", deployRoot),
			false, false)
		add("Run migrations",
			fmt.Sprintf("cd %s && php artisan migrate --force", deployRoot),
			false, false)
		add("Create storage symlink",
			fmt.Sprintf("cd %s && php artisan storage:link", deployRoot),
			false, false)
	}

	// nginx vhost.
	fpmSock := fmt.Sprintf("php%s-fpm.sock", php)
	vhost := fmt.Sprintf(`server {
    listen 80;
    server_name %s;
    root %s/public;
    index index.php index.html;

    location / {
        try_files $uri $uri/ /index.php?$query_string;
    }

    location ~ \\.php$ {
        fastcgi_pass unix:/run/php/%s;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        include fastcgi_params;
    }
}`, domain, deployRoot, fpmSock)

	add("Write nginx vhost",
		fmt.Sprintf(`cat > /etc/nginx/sites-available/%s << 'VHOST'
%s
VHOST`, cfg.ProjectName, vhost), true, false)
	add("Enable site",
		fmt.Sprintf("ln -sf /etc/nginx/sites-available/%s /etc/nginx/sites-enabled/%s", cfg.ProjectName, cfg.ProjectName),
		true, false)
	add("Test nginx configuration", "nginx -t", true, false)
	add("Reload nginx", "systemctl reload nginx", true, false)

	// SSL via certbot.
	if cfg.SSL.Enabled {
		email := cfg.SSL.Email
		if email == "" {
			email = "admin@" + cfg.Domain
		}
		add("Install certbot", "apt-get install -y certbot python3-certbot-nginx", true, false)
		add("Obtain SSL certificate",
			fmt.Sprintf("certbot --nginx -d %s --non-interactive --agree-tos -m %s", domain, email),
			true, true)
	}

	// Auto-deploy cron.
	if cfg.AutoDeployCron != "" {
		add("Install auto-deploy cron",
			fmt.Sprintf(`echo '%s root cd %s && git pull origin %s && composer install --no-dev && php artisan migrate --force' > /etc/cron.d/%s-deploy`,
				cfg.AutoDeployCron, deployRoot, cfg.GitBranch, cfg.ProjectName),
			true, false)
	}

	return steps
}

// BuildDestroyPlan generates the teardown steps for a deployment: the vhost
// and webroot are removed, the site disabled, nginx reloaded, and — only
// when dropDatabase is true — the database dropped. The webroot step moves
// the directory aside instead of rm -rf so a mistaken destroy is still
// recoverable by an operator; the backup is pruned after a week.
func BuildDestroyPlan(cfg ApplyConfig, dropDatabase bool) []Step {
	var steps []Step
	add := func(title, cmd string, sudo, ignore bool) {
		steps = append(steps, Step{Title: title, Command: cmd, Sudo: sudo, Ignore: ignore})
	}

	deployRoot := "/var/www/" + cfg.ProjectName

	add("Disable site",
		fmt.Sprintf("rm -f /etc/nginx/sites-enabled/%s", cfg.ProjectName), true, true)
	add("Remove nginx vhost",
		fmt.Sprintf("rm -f /etc/nginx/sites-available/%s", cfg.ProjectName), true, true)
	add("Remove auto-deploy cron",
		fmt.Sprintf("rm -f /etc/cron.d/%s-deploy", cfg.ProjectName), true, true)
	add("Test nginx configuration", "nginx -t", true, false)
	add("Reload nginx", "systemctl reload nginx", true, false)

	if dropDatabase && cfg.Database.Engine != "" && cfg.Database.Name != "" {
		add("Drop database", databaseDropScript(cfg.Database), true, false)
	}

	add("Archive webroot",
		fmt.Sprintf(`if [ -d %[1]s ]; then
  mkdir -p /var/www/kitodock-destroyed
  mv %[1]s /var/www/kitodock-destroyed/%[2]s-$(date +%%s)
  find /var/www/kitodock-destroyed -maxdepth 1 -type d -name '%[2]s-*' -mtime +7 -exec rm -rf {} +
fi`, deployRoot, cfg.ProjectName),
		true, false)

	return steps
}
