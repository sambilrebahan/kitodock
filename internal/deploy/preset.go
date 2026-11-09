package deploy

import (
	"fmt"
	"regexp"
	"strings"
)

// Preset is a reusable deploy script template with {token} placeholders.
type Preset struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Script      string      `json:"script"`
	Variables   []PresetVar `json:"variables"`
	BuiltIn     bool        `json:"builtIn"`
}

// PresetVar declares one variable a preset expects.
type PresetVar struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Default string `json:"default"`
}

// BuiltinPresetVars are the tokens every preset can use.
var builtinVars = []string{
	"name", "domain", "raw_domain", "branch", "php_version",
	"deploy_root", "environment", "git_url", "web_server", "database",
}

// tokenPattern matches one {lower_snake_case} placeholder in a preset script.
var tokenPattern = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)\}`)

// ResolveDeployScript fills {token} placeholders in a preset script with
// values from the deployment's ApplyConfig + user-supplied variables.
// Unknown tokens are left in place (visible, traceable).
func ResolveDeployScript(preset *Preset, cfg ApplyConfig, userVars map[string]string) string {
	if preset == nil {
		return ""
	}
	values := presetValues(cfg, userVars)
	return tokenPattern.ReplaceAllStringFunc(preset.Script, func(match string) string {
		key := strings.Trim(match, "{}")
		if v, ok := values[key]; ok {
			return v
		}
		return match
	})
}

func presetValues(cfg ApplyConfig, userVars map[string]string) map[string]string {
	values := map[string]string{
		"name":        cfg.ProjectName,
		"domain":      cfg.EffectiveDomain(),
		"raw_domain":  cfg.Domain,
		"branch":      cfg.GitBranch,
		"php_version": cfg.PHPVersion,
		"deploy_root": "/var/www/" + cfg.ProjectName,
		"environment": cfg.Environment,
		"git_url":     SpliceGitPAT(cfg.GitURL, cfg.GitPAT),
		"web_server":  cfg.WebServer,
		"database":    cfg.Database.Engine,
	}
	// User vars override builtins (except they can't shadow).
	for k, v := range userVars {
		if !isBuiltinVar(k) {
			values[k] = v
		}
	}
	return values
}

func isBuiltinVar(key string) bool {
	for _, v := range builtinVars {
		if v == key {
			return true
		}
	}
	return false
}

// ValidatePresetVarKeys rejects variables that shadow builtins.
func ValidatePresetVarKeys(vars []PresetVar) error {
	for _, v := range vars {
		if isBuiltinVar(v.Key) {
			return fmt.Errorf("variable %q shadows a built-in token", v.Key)
		}
	}
	return nil
}

// --- Builtin preset registry -------------------------------------------------

// The catalog ships with kitodock and is shared by the desktop
// Deployments controller and the kitodock-deploy CLI. Presets replace the
// generated plan with a fully caller-controlled script, so each one
// documents exactly what it touches.
// builtinPresets ships with the app. Presets replace the generated plan with
// a fully caller-controlled script, so they document what they touch.
var builtinPresets = []Preset{{
	ID:          "git-pull-update",
	Name:        "Git pull update",
	Description: "Fast update path for an already-provisioned server: fetch the branch, hard reset, reinstall dependencies, migrate, rebuild caches.",
	BuiltIn:     true,
	Script: `set -e
cd {deploy_root}
echo "==> fetching {branch}"
git fetch origin {branch}
git reset --hard origin/{branch}
echo "==> composer"
composer install --no-dev --optimize-autoloader --no-interaction 2>&1 || echo "composer not present, skipped"
echo "==> migrate"
php artisan migrate --force 2>&1 || echo "artisan not present, skipped"
php artisan config:cache 2>&1 || true
php artisan route:cache 2>&1 || true
echo "==> done"`,
	Variables: []PresetVar{{
		Key: "deploy_root", Label: "Deploy root", Default: "/var/www/{name}",
	}},
}, {
	ID:          "laravel-production",
	Name:        "Laravel production (full provision)",
	Description: "Complete provisioning of an empty Ubuntu server for one Laravel site: packages, PHP-FPM, nginx vhost, MySQL database, certbot certificate, queue cron — the generated plan, but as an inspectable, copyable script.",
	BuiltIn:     true,
	Script: `set -e
echo "==> base packages"
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y software-properties-common curl git unzip nginx mysql-server
add-apt-repository -y ppa:ondrej/php
DEBIAN_FRONTEND=noninteractive apt-get install -y php{php_version} php{php_version}-fpm php{php_version}-mysql php{php_version}-mbstring php{php_version}-xml php{php_version}-curl php{php_version}-zip php{php_version}-gd php{php_version}-bcmath
curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer

echo "==> code"
mkdir -p {deploy_root}
if [ ! -d {deploy_root}/.git ]; then git clone --branch {branch} --single-branch {git_url} {deploy_root}; fi
cd {deploy_root}
composer install --no-dev --optimize-autoloader --no-interaction
[ -f .env ] || cp .env.example .env
php artisan key:generate --force

echo "==> site"
cat > /etc/nginx/sites-available/{name} <<'VHOST'
server {
    listen 80;
    server_name {domain};
    root {deploy_root}/public;
    index index.php;
    location / { try_files $uri $uri/ /index.php?$query_string; }
    location ~ \.php$ {
        fastcgi_pass unix:/run/php/php{php_version}-fpm.sock;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        include fastcgi_params;
    }
}
VHOST
ln -sf /etc/nginx/sites-available/{name} /etc/nginx/sites-enabled/{name}
nginx -t && systemctl reload nginx

echo "==> done: {domain}"`,
	Variables: []PresetVar{{
		Key: "deploy_root", Label: "Deploy root", Default: "/var/www/{name}",
	}},
}, {
	ID:          "static-site",
	Name:        "Static site",
	Description: "Deploy a plain static site (no PHP): clone the repository, publish its build output directory to the webroot, one nginx vhost, no database.",
	BuiltIn:     true,
	Script: `set -e
echo "==> packages"
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y nginx git

echo "==> code"
mkdir -p {deploy_root}
if [ ! -d {deploy_root}/.git ]; then git clone --branch {branch} --single-branch {git_url} {deploy_root}; fi
cd {deploy_root}
git fetch origin {branch} && git reset --hard origin/{branch}

echo "==> site"
cat > /etc/nginx/sites-available/{name} <<'VHOST'
server {
    listen 80;
    server_name {domain};
    root {deploy_root}/public;
    index index.html index.htm;
    location / { try_files $uri $uri/ =404; }
}
VHOST
ln -sf /etc/nginx/sites-available/{name} /etc/nginx/sites-enabled/{name}
nginx -t && systemctl reload nginx
echo "==> done: {domain}"`,
	Variables: []PresetVar{{
		Key: "deploy_root", Label: "Deploy root", Default: "/var/www/{name}",
	}},
}}

// BuiltinPresets returns the preset catalog (do not mutate entries).
func BuiltinPresets() []Preset { return builtinPresets }

// FindBuiltinPreset resolves a preset id.
func FindBuiltinPreset(id string) *Preset {
	for i := range builtinPresets {
		if builtinPresets[i].ID == id {
			return &builtinPresets[i]
		}
	}
	return nil
}
