package controllers

// The Site Doctor controller: diagnose a project's configuration and
// apply individual fixes with streamed output.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"kitodock/internal/sitedoctor"
)

// Doctor is the diagnosis/repair controller.
type Doctor struct {
	bus *EventBus
	// resolve turns a project name into a sitedoctor.Target.
	resolve func(name string) sitedoctor.Target
}

func NewDoctor(bus *EventBus, resolve func(string) sitedoctor.Target) *Doctor {
	return &Doctor{bus: bus, resolve: resolve}
}

// Diagnose runs every check for one project.
func (d *Doctor) Diagnose(name string) sitedoctor.Report {
	return sitedoctor.Check(d.resolve(name), sitedoctor.Dependencies{})
}

// DiagnoseAll runs every check for every project.
func (d *Doctor) DiagnoseAll(names []string) map[string]sitedoctor.Report {
	out := map[string]sitedoctor.Report{}
	for _, name := range names {
		out[name] = d.Diagnose(name)
	}
	return out
}

// ApplyFix runs one named repair, streaming output lines as doctor:output
// events. The fix implementations live here because they need process
// spawning (composer, npm) that sitedoctor deliberately does not do.
func (d *Doctor) ApplyFix(name, fixID string) error {
	target := d.resolve(name)
	d.bus.Emit(EvDoctorOutput, map[string]any{"project": name, "line": fmt.Sprintf("applying fix %s…", fixID)})

	var err error
	switch fixID {
	case "env":
		err = d.fixEnv(name, target)
	case "app_key":
		err = d.fixAppKey(name, target)
	case "composer_dependencies":
		err = d.fixComposer(name, target)
	case "node_dependencies":
		err = d.fixNodeModules(name, target)
	case "storage_link":
		err = d.fixStorageLink(name, target)
	case "vhost":
		d.bus.Emit(EvDoctorOutput, map[string]any{"project": name, "line": "re-applying serving configuration"})
		d.bus.Emit(EvProjectChanged, map[string]any{"kind": "rescan", "name": name})
	case "hosts":
		d.bus.Emit(EvDoctorOutput, map[string]any{"project": name, "line": "re-applying hosts entry"})
		d.bus.Emit(EvProjectChanged, map[string]any{"kind": "rescan", "name": name})
	case "php_fpm":
		d.bus.Emit(EvDoctorOutput, map[string]any{"project": name, "line": "php-fpm pool will start on next serve"})
	default:
		err = fmt.Errorf("unknown fix %s", fixID)
	}
	if err != nil {
		d.bus.Emit(EvDoctorOutput, map[string]any{"project": name, "line": err.Error()})
		return err
	}
	d.bus.Emit(EvDoctorOutput, map[string]any{"project": name, "line": "done"})
	return nil
}

func (d *Doctor) fixEnv(name string, target sitedoctor.Target) error {
	source := filepath.Join(target.Path, ".env.example")
	dest := filepath.Join(target.Path, ".env")
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf(".env.example not found")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	d.line(name, "copying .env.example → .env")
	return os.WriteFile(dest, data, 0o644)
}

func (d *Doctor) fixAppKey(name string, target sitedoctor.Target) error {
	if target.PhpExe == "" {
		return fmt.Errorf("no PHP runtime resolved")
	}
	d.line(name, "generating APP_KEY…")
	out, err := d.run(target.PhpExe, target.Path, "-r", "echo base64_encode(random_bytes(32));")
	if err != nil {
		return fmt.Errorf("key generation failed: %v", err)
	}
	key := strings.TrimSpace(string(out))
	return d.setEnvValue(target.Path, "APP_KEY", "base64:"+key)
}

func (d *Doctor) fixComposer(name string, target sitedoctor.Target) error {
	if target.PhpExe == "" {
		return fmt.Errorf("no PHP runtime resolved")
	}
	composer := findComposer(target.PhpExe)
	if composer == "" {
		return fmt.Errorf("composer not found")
	}
	d.line(name, "composer install…")
	// findComposer may yield "php.exe phar" — an argv, not a binary.
	return d.streamArgv(name, strings.Fields(composer), target.Path, "install", "--no-interaction")
}

func (d *Doctor) fixNodeModules(name string, target sitedoctor.Target) error {
	pm := "npm"
	if _, err := os.Stat(filepath.Join(target.Path, "pnpm-lock.yaml")); err == nil {
		pm = "pnpm"
	} else if _, err := os.Stat(filepath.Join(target.Path, "yarn.lock")); err == nil {
		pm = "yarn"
	}
	d.line(name, pm+" install…")
	return d.stream(name, pm, target.Path, "install")
}

func (d *Doctor) fixStorageLink(name string, target sitedoctor.Target) error {
	if target.PhpExe == "" {
		return fmt.Errorf("no PHP runtime resolved")
	}
	d.line(name, "php artisan storage:link…")
	return d.stream(name, target.PhpExe, target.Path, "artisan", "storage:link")
}

// setEnvValue sets key=value in .env, creating the file if needed.
func (d *Doctor) setEnvValue(projectPath, key, value string) error {
	envPath := filepath.Join(projectPath, ".env")
	data, _ := os.ReadFile(envPath)
	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), key+"=") {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	return os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0o644)
}

// run executes a command and returns its stdout.
func (d *Doctor) run(exe, dir string, args ...string) ([]byte, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.CombinedOutput()
}

// stream runs a command and sends each output line as an event.
// streamArgv runs a command whose first tokens are already an argv.
func (d *Doctor) streamArgv(name string, argv []string, dir string, args ...string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	full := append(append([]string{}, argv[1:]...), args...)
	return d.stream(name, argv[0], dir, full...)
}

func (d *Doctor) stream(project, exe, dir string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		if line != "" {
			d.line(project, line)
		}
	}
	return err
}

func (d *Doctor) line(project, text string) {
	d.bus.Emit(EvDoctorOutput, map[string]any{"project": project, "line": text})
}

// findComposer locates a composer binary near the PHP runtime.
func findComposer(phpExe string) string {
	// The bundled phar sits next to the environment root.
	phar := filepath.Join(filepath.Dir(filepath.Dir(phpExe)), "composer", "composer.phar")
	if _, err := os.Stat(phar); err == nil {
		return phpExe + " " + phar
	}
	if path, err := exec.LookPath("composer"); err == nil {
		return path
	}
	return ""
}
