package deploy

// Database provisioning: access normalization plus the embedded SQL/bash
// snippets BuildPlan splices in for MySQL and PostgreSQL. Passwords only
// travel inside the generated commands; everything surfaced for display
// goes through the Redactor.

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// DefaultPorts per engine, applied by NormalizeDatabaseAccess.
const (
	DefaultMySQLPort    = 3306
	DefaultPostgresPort = 5432
)

// normalizeDefaultUser derives the conventional superuser name per engine.
func normalizeDefaultUser(engine string) string {
	if engine == "postgres" {
		return "postgres"
	}
	return "root"
}

// NormalizeDatabaseAccess fills defaults and validates the database block:
// engine-specific port and user, a non-empty generated password when the
// caller left it blank, and a database name safe for shell interpolation.
// It returns a copy; the receiver's fields are untouched.
func NormalizeDatabaseAccess(db Database) (Database, error) {
	switch db.Engine {
	case "", "mysql", "postgres":
	default:
		return db, fmt.Errorf("unsupported database engine %q (use mysql or postgres)", db.Engine)
	}

	if db.Engine == "" {
		return db, nil // database explicitly disabled
	}

	if db.AccessPort == 0 {
		if db.Engine == "postgres" {
			db.AccessPort = DefaultPostgresPort
		} else {
			db.AccessPort = DefaultMySQLPort
		}
	}
	if db.AccessHost == "" {
		db.AccessHost = "127.0.0.1"
	}
	if db.User == "" {
		db.User = normalizeDefaultUser(db.Engine)
	}
	if db.Name == "" {
		return db, fmt.Errorf("database name is required when engine %q is selected", db.Engine)
	}
	if strings.ContainsAny(db.Name, " '\"\\\n;$&|<>()`") {
		return db, fmt.Errorf("database name %q contains characters unsafe for scripts", db.Name)
	}
	// A generated password keeps its shape shell-safe on purpose: only
	// letters and digits, so no quoting edge cases in generated SQL/bash.
	if db.Password == "" {
		db.Password = generateDatabasePassword()
	} else if err := ValidateDatabasePassword(db.Password); err != nil {
		return db, err
	}
	return db, nil
}

// generateDatabasePassword produces a random alphanumeric secret. It is
// provisioned once per deployment and stored sealed; it never guards
// anything local, so the alphabet keeps it shell-safe without quoting.
func generateDatabasePassword() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"
	const length = 24
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		// Degraded-entropy fallback: derive from the clock rather than
		// store an empty password the user never chose.
		seed := time.Now().UnixNano()
		for i := range buf {
			buf[i] = byte(seed >> (uint(i%8) * 8))
		}
	}
	var b strings.Builder
	b.Grow(length)
	for _, v := range buf {
		b.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return b.String()
}

// databaseCreateScript renders the engine-specific create+grant statements
// for one deployment. The app user gets full rights on its own database
// only; superuser stays for migrations run by the deploy step.
func databaseCreateScript(db Database, deployRoot string) string {
	safeName := db.Name
	switch db.Engine {
	case "postgres":
		return fmt.Sprintf(`sudo -n -u postgres psql <<'SQL'
SELECT 'CREATE DATABASE %s' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '%s')\gexec
SELECT 'CREATE ROLE %s LOGIN PASSWORD ''%s''' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s')\gexec
GRANT ALL PRIVILEGES ON DATABASE %s TO %s;
SQL`, safeName, safeName, appUser(safeName), db.Password, appUser(safeName), safeName, appUser(safeName))
	default: // mysql
		return fmt.Sprintf(`mysql -u root <<'SQL'
CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s';
CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';
GRANT ALL PRIVILEGES ON %s.* TO '%s'@'localhost';
GRANT ALL PRIVILEGES ON %s.* TO '%s'@'%%';
FLUSH PRIVILEGES;
SQL`, safeName, appUser(safeName), db.Password, appUser(safeName), db.Password,
			safeName, appUser(safeName), safeName, appUser(safeName))
	}
}

// databaseEnvSnippet renders the .env lines pointing the application at
// its database, appended to the deploy root's .env when absent.
func databaseEnvSnippet(db Database) string {
	if db.Engine == "postgres" {
		return fmt.Sprintf(`DB_CONNECTION=pgsql
DB_HOST=%s
DB_PORT=%d
DB_DATABASE=%s
DB_USERNAME=%s
DB_PASSWORD=%s`,
			db.AccessHost, db.AccessPort, db.Name, appUser(db.Name), db.Password)
	}
	return fmt.Sprintf(`DB_CONNECTION=mysql
DB_HOST=%s
DB_PORT=%d
DB_DATABASE=%s
DB_USERNAME=%s
DB_PASSWORD=%s`,
		db.AccessHost, db.AccessPort, db.Name, appUser(db.Name), db.Password)
}

// appUser derives the per-application database user from the database name.
func appUser(dbName string) string {
	name := strings.ToLower(dbName)
	name = strings.ReplaceAll(name, "-", "_")
	if len(name) > 24 {
		name = name[:24]
	}
	return name + "_app"
}

// databaseDropScript is the destructive counterpart used by destroy plans.
func databaseDropScript(db Database) string {
	if db.Engine == "postgres" {
		return fmt.Sprintf(`sudo -n -u postgres psql <<'SQL'
DROP DATABASE IF EXISTS %s;
DROP ROLE IF EXISTS %s;
SQL`, db.Name, appUser(db.Name))
	}
	return fmt.Sprintf(`mysql -u root <<'SQL'
DROP DATABASE IF EXISTS %s;
DROP USER IF EXISTS '%s'@'localhost';
DROP USER IF EXISTS '%s'@'%%';
FLUSH PRIVILEGES;
SQL`, db.Name, appUser(db.Name), appUser(db.Name))
}
