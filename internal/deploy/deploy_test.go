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

	steps := BuildPlan(cfg)
	if len(steps) < 10 {
		t.Fatalf("plan = %d steps, want substantial", len(steps))
	}

	// Verify key steps are present.
	titles := map[string]bool{}
	for _, s := range steps {
		titles[s.Title] = true
	}
	for _, want := range []string{
		"Update package list", "Install PHP 8.3", "Install nginx",
		"Clone repository", "Run migrations", "Write nginx vhost",
		"Obtain SSL certificate",
	} {
		if !titles[want] {
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
