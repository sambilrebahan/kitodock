package deploy

// Input validation for everything the plan builder interpolates into
// remote bash. Every field is checked against a strict character set at
// the door — plan strings are executed by a root shell on the target, so
// "escape carefully" is not enough; the value either fits its grammar or
// the config is refused.

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	domainRe   = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$`)
	branchRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	gitURLRe   = regexp.MustCompile(`^(https://|git@)[A-Za-z0-9:@._~+/\\-]+$`)
	phpVerRe   = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	cronLineRe = regexp.MustCompile(`^[0-9*/,\- 	A-Za-z]{1,60}$`)
	emailRe    = regexp.MustCompile(`^[^@\s;'"\\$&|()<>]{1,64}@[^@\s;'"\\$&|()<>]{1,190}$`)
	patRe      = regexp.MustCompile(`^[A-Za-z0-9_\-]{8,255}$`)
)

// ValidateApplyConfig rejects any config whose values could smuggle shell
// or SQL metacharacters into generated scripts.
func ValidateApplyConfig(cfg ApplyConfig) error {
	if !nameRe.MatchString(cfg.ProjectName) {
		return fmt.Errorf("project name %q: lowercase letters, digits, - and _ only", cfg.ProjectName)
	}
	if !domainRe.MatchString(cfg.Domain) {
		return fmt.Errorf("domain %q is not a valid hostname", cfg.Domain)
	}
	switch cfg.Environment {
	case "", "production", "staging", "development":
	default:
		return fmt.Errorf("environment %q not allowed", cfg.Environment)
	}
	switch cfg.Kind {
	case "", "laravel", "static":
	default:
		return fmt.Errorf("kind %q not allowed", cfg.Kind)
	}
	if cfg.GitURL != "" && !gitURLRe.MatchString(cfg.GitURL) {
		return fmt.Errorf("git url %q rejected (https:// or git@ URLs only, no query strings)", cfg.GitURL)
	}
	if cfg.GitPAT != "" && !patRe.MatchString(cfg.GitPAT) {
		return fmt.Errorf("git token rejected: letters, digits, - and _ only")
	}
	if cfg.GitBranch != "" && !branchRe.MatchString(cfg.GitBranch) {
		return fmt.Errorf("git branch %q rejected", cfg.GitBranch)
	}
	if cfg.PHPVersion != "" && !phpVerRe.MatchString(cfg.PHPVersion) {
		return fmt.Errorf("php version %q must look like 8.3", cfg.PHPVersion)
	}
	if cfg.WebServer != "" && cfg.WebServer != "nginx" {
		return fmt.Errorf("web server %q not allowed", cfg.WebServer)
	}
	if cfg.AutoDeployCron != "" && !cronLineRe.MatchString(cfg.AutoDeployCron) {
		return fmt.Errorf("cron expression %q rejected", cfg.AutoDeployCron)
	}
	if cfg.SSL.Enabled && cfg.SSL.Email != "" && !emailRe.MatchString(cfg.SSL.Email) {
		return fmt.Errorf("ssl email %q rejected", cfg.SSL.Email)
	}
	if err := ValidateDatabase(cfg.Database); err != nil {
		return err
	}
	return nil
}

// ValidateDatabase guards the block separately: names feed SQL and paths,
// passwords feed both SQL heredocs and .env lines.
func ValidateDatabase(db Database) error {
	switch db.Engine {
	case "", "mysql", "postgres":
	default:
		return fmt.Errorf("database engine %q not allowed", db.Engine)
	}
	if db.Engine == "" {
		return nil
	}
	if !nameRe.MatchString(db.Name) {
		return fmt.Errorf("database name %q: lowercase letters, digits, - and _ only", db.Name)
	}
	if db.User != "" && !nameRe.MatchString(db.User) {
		return fmt.Errorf("database user %q rejected", db.User)
	}
	if err := ValidateDatabasePassword(db.Password); err != nil {
		return err
	}
	if db.AccessHost != "" && !domainRe.MatchString(db.AccessHost) && db.AccessHost != "127.0.0.1" && !isIPv4(db.AccessHost) {
		return fmt.Errorf("database host %q rejected", db.AccessHost)
	}
	if db.AccessPort < 0 || db.AccessPort > 65535 {
		return fmt.Errorf("database port %d out of range", db.AccessPort)
	}
	return nil
}

// ValidateDatabasePassword keeps credentials injectable into SQL heredocs
// and .env files without quoting games: printable, no quotes, no
// expansion, no control characters.
func ValidateDatabasePassword(password string) error {
	if password == "" {
		return nil
	}
	for _, r := range password {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("database password must be alphanumeric (generated ones already are)")
		}
	}
	if len(password) < 8 {
		return fmt.Errorf("database password too short")
	}
	return nil
}

func isIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
