// Command kitodock-deploy provisions and updates a server from the
// terminal using the same engine as the desktop app: detection, the
// generated plan (or a builtin preset), streaming output, and teardown.
// It is the headless sibling of the Deployments page for scripts and CI.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"kitodock/internal/deploy"
	"kitodock/internal/ssh"
)

func main() {
	var (
		hostName = flag.String("host", "", "saved host name from kitodock's ssh hosts.json")
		address  = flag.String("address", "", "SSH address (alternative to -host)")
		user     = flag.String("user", "root", "SSH user with -address")
		port     = flag.Int("port", 22, "SSH port")

		project = flag.String("project", "", "project name (required)")
		domain  = flag.String("domain", "", "primary domain (required)")
		branch  = flag.String("branch", "main", "git branch")
		gitURL  = flag.String("git-url", "", "repository HTTPS URL")
		gitPAT  = flag.String("git-pat", "", "access token (or KITODOCK_GIT_PAT)")

		kind     = flag.String("kind", "laravel", "project kind: laravel | static")
		php      = flag.String("php", "8.3", "PHP series for laravel")
		env      = flag.String("env", "production", "production | staging | development")
		dbEngine = flag.String("db", "mysql", "database engine: mysql | postgres | none")
		dbName   = flag.String("db-name", "", "database name (defaults to the project name)")
		sslEmail = flag.String("ssl-email", "", "request a certbot certificate for this email")

		preset = flag.String("preset", "", "builtin preset id (see -list-presets)")
		list   = flag.Bool("list-presets", false, "print builtin preset ids and exit")

		detect  = flag.Bool("detect", false, "only probe the server and print the report")
		destroy = flag.Bool("destroy", false, "run the teardown plan instead of apply")
		dropDB  = flag.Bool("drop-db", false, "with -destroy: also drop the database")
	)
	flag.Parse()

	if *list {
		for _, p := range deploy.BuiltinPresets() {
			fmt.Printf("%-20s %s\n", p.ID, p.Description)
		}
		return
	}

	pat := *gitPAT
	if pat == "" {
		pat = os.Getenv("KITODOCK_GIT_PAT")
	}

	if err := run(cfg{
		hostName: *hostName, address: *address, port: *port, user: *user,
		project: *project, domain: *domain, branch: *branch, gitURL: *gitURL, pat: pat,
		kind: *kind, php: *php, env: *env, dbEngine: *dbEngine, dbName: *dbName,
		sslEmail: *sslEmail, preset: *preset,
		detect: *detect, destroy: *destroy, dropDB: *dropDB,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "kitodock-deploy:", err)
		os.Exit(1)
	}
}

type cfg struct {
	hostName, address string
	port              int
	user              string

	project, domain, branch, gitURL, pat string
	kind, php, env, dbEngine, dbName     string
	sslEmail, preset                     string

	detect, destroy, dropDB bool
}

func (c cfg) applyConfig() deploy.ApplyConfig {
	config := deploy.ApplyConfig{
		ProjectName: c.project, Kind: c.kind, Domain: c.domain,
		Environment: c.env, GitURL: c.gitURL, GitBranch: c.branch,
		GitPAT: c.pat, PHPVersion: c.php,
	}
	if c.dbEngine != "none" {
		config.Database.Engine = c.dbEngine
		config.Database.Name = c.dbName
		if config.Database.Name == "" {
			config.Database.Name = c.project
		}
	}
	if c.sslEmail != "" {
		config.SSL.Enabled = true
		config.SSL.Email = c.sslEmail
	}
	return config
}

func run(c cfg) error {
	if c.project == "" || c.domain == "" {
		return fmt.Errorf("-project and -domain are required")
	}
	if c.preset != "" && deploy.FindBuiltinPreset(c.preset) == nil {
		return fmt.Errorf("unknown preset %q (try -list-presets)", c.preset)
	}
	session, err := dial(c)
	if err != nil {
		return err
	}
	defer session.Close()

	if c.detect {
		report, err := deploy.RunDetection(session)
		if err != nil {
			return err
		}
		fmt.Printf("os:        %s %s\n", report.OS, report.OSVersion)
		fmt.Printf("nginx:     %v\n", report.HasNginx)
		fmt.Printf("php:       %v (%s)\n", report.HasPHP, report.PHPVersion)
		fmt.Printf("git:       %v\n", report.HasGit)
		fmt.Printf("mysql:     %v\n", report.HasMySQL)
		fmt.Printf("composer:  %v\n", report.HasComposer)
		fmt.Printf("certbot:   %v\n", report.HasCertbot)
		fmt.Printf("fresh vps: %v\n", report.IsFresh)
		return nil
	}

	config := c.applyConfig()
	redactor := deploy.RedactorFor(config)
	var steps []deploy.Step
	switch {
	case c.destroy:
		steps = deploy.BuildDestroyPlan(config, c.dropDB)
	case c.preset != "":
		preset := deploy.FindBuiltinPreset(c.preset)
		steps = []deploy.Step{{
			Title:   preset.Name,
			Command: deploy.ResolveDeployScript(preset, config, nil),
		}}
	default:
		steps = deploy.BuildPlan(config)
	}

	onStep := func(i int, title, status string) {
		fmt.Printf("[%d/%d] %-44s %s\n", i+1, len(steps), title, status)
	}
	_, err = deploy.RunApply(session, steps, onStep, func(line string) {
		fmt.Println("  " + redactor.Redact(line))
	})
	if err != nil {
		return fmt.Errorf("%s", redactor.Redact(err.Error()))
	}
	return nil
}

// dial opens the SSH session: by saved host name (reusing kitodock's
// hosts.json, keys and known_hosts) or by raw address with a password
// from KITODOCK_SSH_PASSWORD.
func dial(c cfg) (*ssh.Session, error) {
	if c.address != "" {
		password := os.Getenv("KITODOCK_SSH_PASSWORD")
		if password == "" {
			return nil, fmt.Errorf("-address mode needs KITODOCK_SSH_PASSWORD set")
		}
		host := ssh.Host{
			Name: c.address, Address: c.address, Port: c.port,
			User: c.user, AuthMode: "password",
		}
		// Raw-address mode takes the server key on trust, like a first
		// ssh connection without known_hosts; saved hosts stay verified.
		return ssh.Connect(host, nil, func(sealed string) (string, error) { return sealed, nil }, nil)
	}
	if c.hostName == "" {
		return nil, fmt.Errorf("give either -host <saved name> or -address <ip>")
	}
	dir := sshStateDir()
	store, err := ssh.LoadHostStore(filepath.Join(dir, "hosts.json"))
	if err != nil {
		return nil, err
	}
	host, ok := store.Find(c.hostName)
	if !ok {
		return nil, fmt.Errorf("host %q is not saved in kitodock", c.hostName)
	}
	var keyData []byte
	if host.AuthMode == "key" {
		if keyData, err = os.ReadFile(filepath.Join(dir, "keys", host.KeyName)); err != nil {
			return nil, err
		}
	}
	known, err := ssh.LoadKnownHosts(filepath.Join(dir, "known_hosts.json"))
	if err != nil {
		return nil, err
	}
	return ssh.Connect(host, keyData, envUnsealer(), known.Callback())
}

// envUnsealer reads the password from the environment; saved sealed
// passwords stay sealed unless kitodock itself is running.
func envUnsealer() func(string) (string, error) {
	password := os.Getenv("KITODOCK_SSH_PASSWORD")
	return func(string) (string, error) {
		if password != "" {
			return password, nil
		}
		return "", fmt.Errorf("host uses a sealed password; set KITODOCK_SSH_PASSWORD or switch the host to key auth")
	}
}

// sshStateDir resolves kitodock's ssh state directory; KITODOCK_ROOT
// overrides for containers and CI.
func sshStateDir() string {
	if root := os.Getenv("KITODOCK_ROOT"); root != "" {
		return filepath.Join(root, "ssh")
	}
	exe, err := os.Executable()
	if err != nil {
		return "ssh"
	}
	return filepath.Join(filepath.Dir(exe), "ssh")
}
