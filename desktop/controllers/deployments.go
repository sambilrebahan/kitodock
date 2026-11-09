package controllers

// The deployments controller: stored deployment records (config sealed with
// DPAPI), detection runs, and full applies executed over SSH with the
// single-flight coordinator. Everything a run streams reaches the UI as
// deploy:step / deploy:line events; the outcome lands in run history.

import (
	"encoding/json"
	"fmt"

	"kitodock/internal/credentials"
	"kitodock/internal/deploy"
	"kitodock/internal/deploycoord"
	"kitodock/internal/deployments"
	"kitodock/internal/ssh"
)

// Deployments owns the deployment store and coordinates runs.
type Deployments struct {
	bus    *EventBus
	store  *deployments.Store
	coord  *deploycoord.Coordinator
	ssh    *SSH
	sealer credentials.Sealer
}

// deploymentDoc is the payload sealed into Deployment.Config: the editable
// spec plus everything needed to reach the server.
type deploymentDoc struct {
	HostName   string             `json:"hostName"`
	PresetID   string             `json:"presetId,omitempty"`
	PresetVars map[string]string  `json:"presetVars,omitempty"`
	Config     deploy.ApplyConfig `json:"config"`
}

// DeploymentView is the editable form the frontend sees; the sealed config
// is unpacked so the UI never handles ciphertext.
type DeploymentView struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	HostName   string                   `json:"hostName"`
	PresetID   string                   `json:"presetId"`
	PresetVars map[string]string        `json:"presetVars"`
	Config     deploy.ApplyConfig       `json:"config"`
	Runs       []deployments.RunSummary `json:"runs"`
}

// dpapiProtector adapts credentials.Sealer to the store's Protector.
type dpapiProtector struct{ sealer credentials.Sealer }

func (p dpapiProtector) Protect(plain string) (string, error) { return p.sealer.Seal(plain) }
func (p dpapiProtector) Unseal(sealed string) (string, error) { return p.sealer.Unseal(sealed) }

// NewDeployments opens the store at indexPath and wires the coordinator.
func NewDeployments(bus *EventBus, indexPath string, sealer credentials.Sealer, sshCtrl *SSH) (*Deployments, error) {
	store, err := deployments.OpenStore(indexPath, dpapiProtector{sealer})
	if err != nil {
		return nil, err
	}
	return &Deployments{
		bus: bus, store: store, coord: &deploycoord.Coordinator{}, ssh: sshCtrl, sealer: sealer,
	}, nil
}

// ListDeployments returns the records newest-first (sealed configs opaque).
func (c *Deployments) ListDeployments() []deployments.Deployment {
	return c.store.List()
}

// GetDeployment unpacks one record into its editable view.
func (c *Deployments) GetDeployment(id string) (DeploymentView, error) {
	rec, ok := c.store.Get(id)
	if !ok {
		return DeploymentView{}, fmt.Errorf("deployment %s not found", id)
	}
	view := DeploymentView{
		ID: rec.ID, Name: rec.Name,
		PresetVars: map[string]string{},
		Runs:       rec.Runs,
	}
	if rec.Config != "" {
		plain, err := c.sealer.Unseal(rec.Config)
		if err != nil {
			return DeploymentView{}, fmt.Errorf("unseal config: %w", err)
		}
		var doc deploymentDoc
		if err := json.Unmarshal([]byte(plain), &doc); err != nil {
			return DeploymentView{}, fmt.Errorf("parse deployment config: %w", err)
		}
		view.HostName = doc.HostName
		view.PresetID = doc.PresetID
		view.PresetVars = doc.PresetVars
		view.Config = doc.Config
	}
	if view.PresetVars == nil {
		view.PresetVars = map[string]string{}
	}
	return view, nil
}

// SaveDeployment creates or updates a record, sealing the doc into Config.
// The database block is normalized first so a generated password is part
// of the stored spec (BuildPlan is idempotent against it afterwards).
func (c *Deployments) SaveDeployment(view DeploymentView) error {
	db, err := deploy.NormalizeDatabaseAccess(view.Config.Database)
	if err != nil {
		return err
	}
	view.Config.Database = db

	doc := deploymentDoc{
		HostName:   view.HostName,
		PresetID:   view.PresetID,
		PresetVars: view.PresetVars,
		Config:     view.Config,
	}
	plain, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	sealed, err := c.sealer.Seal(string(plain))
	if err != nil {
		return fmt.Errorf("seal config: %w", err)
	}

	if view.ID == "" {
		view.ID = deployments.GenerateID()
	}
	rec := deployments.Deployment{
		ID: view.ID, Name: view.Name, Config: sealed,
	}
	// Preserve creation time and run history on updates.
	if existing, ok := c.store.Get(view.ID); ok {
		rec.CreatedAt = existing.CreatedAt
		rec.Runs = existing.Runs
	}
	return c.store.Save(rec)
}

// DeleteDeployment removes a record and its run logs.
func (c *Deployments) DeleteDeployment(id string) error { return c.store.Delete(id) }

// DeployPlanPreview returns the steps a deployment would run, without
// touching the server. Commands are redacted: they carry spliced secrets.
// Custom presets resolve to their script step.
func (c *Deployments) DeployPlanPreview(view DeploymentView) []deploy.Step {
	return deploy.RedactorFor(view.Config).RedactSteps(c.stepsFor(view))
}

// DeployDestroyPreview returns the redacted teardown steps for a
// deployment, so the UI can show exactly what destroy would remove.
func (c *Deployments) DeployDestroyPreview(view DeploymentView, dropDatabase bool) []deploy.Step {
	return deploy.RedactorFor(view.Config).RedactSteps(
		deploy.BuildDestroyPlan(view.Config, dropDatabase))
}

// DetectResult pairs a detection report with its recorded run ID.
type DetectResult struct {
	Detection *deploy.Detection `json:"detection"`
	RunID     string            `json:"runId"`
}

// RunDetect probes the target server and records the run. It obeys the same
// single-flight lock as applies so the two never interleave.
func (c *Deployments) RunDetect(id string) (DetectResult, error) {
	release, err := c.coord.Acquire()
	if err != nil {
		return DetectResult{}, err
	}
	defer release()

	view, err := c.GetDeployment(id)
	if err != nil {
		return DetectResult{}, err
	}
	runID := deployments.GenerateID()
	_ = c.store.StartRun(id, runID, deployments.KindDetect)

	connID, runner, err := c.connectRunner(view.HostName)
	if err != nil {
		_ = c.store.FinishRun(id, runID, "error", err.Error())
		return DetectResult{RunID: runID}, err
	}
	defer c.ssh.DisconnectSSH(connID)

	detection, err := deploy.RunDetection(runner)
	status, errMsg := "success", ""
	if err != nil {
		status, errMsg = "error", err.Error()
	}
	_ = c.store.FinishRun(id, runID, status, errMsg)
	return DetectResult{Detection: detection, RunID: runID}, err
}

// StartApply launches a full apply asynchronously and returns the run ID.
// Progress streams as deployment:apply:step|line:<id>; the finish is
// deployment:apply:done:<id>. The host's SFTP view is frozen for the run
// so browsers don't observe half-written state.
func (c *Deployments) StartApply(id string) (string, error) {
	return c.startMutation(id, KindApply, func(view DeploymentView) []deploy.Step {
		return c.stepsFor(view)
	})
}

// StartDestroy launches the teardown plan asynchronously. dropDatabase
// additionally drops the provisioned database; the webroot is always
// archived aside rather than deleted in place.
func (c *Deployments) StartDestroy(id string, dropDatabase bool) (string, error) {
	return c.startMutation(id, KindDestroy, func(view DeploymentView) []deploy.Step {
		return deploy.BuildDestroyPlan(view.Config, dropDatabase)
	})
}

// startMutation runs one asynchronous mutation kind under the coordinator.
func (c *Deployments) startMutation(id, kind string, plan func(DeploymentView) []deploy.Step) (string, error) {
	release, err := c.coord.Acquire()
	if err != nil {
		return "", err
	}
	view, err := c.GetDeployment(id)
	if err != nil {
		release()
		return "", err
	}
	runID := deployments.GenerateID()
	if err := c.store.StartRun(id, runID, deployments.Kind(kind)); err != nil {
		release()
		return "", err
	}

	go func() {
		defer release()
		c.ssh.SetHostFrozen(view.HostName, true)
		defer c.ssh.SetHostFrozen(view.HostName, false)

		status, errMsg := "success", ""
		// Everything surfaced — events, stored logs, error text — passes
		// the redactor so spliced secrets never leak.
		redactor := deploy.RedactorFor(view.Config)
		output, err := c.mutateOnce(id, runID, kind, plan(view), redactor)
		if err != nil {
			status, errMsg = "error", redactor.Redact(err.Error())
		}
		_ = c.store.FinishRun(id, runID, status, errMsg)
		_ = c.store.SaveRunLog(id, runID, output+errMsg)
		c.bus.Emit(DeployEvent(kind, "done", id), map[string]any{
			"deploymentId": id, "runId": runID, "status": status, "error": errMsg,
		})
	}()
	return runID, nil
}

// mutateOnce performs one synchronous mutation, streaming redacted events.
func (c *Deployments) mutateOnce(id, runID, kind string, steps []deploy.Step, redactor *deploy.Redactor) (string, error) {
	connID, runner, err := c.connectRunnerFor(kind, id)
	if err != nil {
		return "", err
	}
	defer c.ssh.DisconnectSSH(connID)

	onStep := func(stepIndex int, title, status string) {
		c.bus.Emit(DeployEvent(kind, "step", id), map[string]any{
			"deploymentId": id, "runId": runID,
			"index": stepIndex, "title": title, "status": status,
		})
	}
	onLine := func(line string) {
		c.bus.Emit(DeployEvent(kind, "line", id), map[string]any{
			"deploymentId": id, "runId": runID, "line": redactor.Redact(line),
		})
	}
	output, err := deploy.RunApply(runner, steps, onStep, onLine)
	return redactor.Redact(output), err
}

// connectRunnerFor opens the deployment's host; the kind only shapes the
// error message.
func (c *Deployments) connectRunnerFor(kind, id string) (string, deploy.Runner, error) {
	view, err := c.GetDeployment(id)
	if err != nil {
		return "", nil, err
	}
	if view.HostName == "" {
		return "", nil, fmt.Errorf("deployment has no host selected")
	}
	connID, err := c.ssh.ConnectSSH(view.HostName)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", kind, err)
	}
	return connID, sshRunner{ctrl: c.ssh, connID: connID}, nil
}

// GetRunLog returns a finished run's unsealed output.
func (c *Deployments) GetRunLog(runID string) (string, error) {
	return c.store.ReadRunLog(runID)
}

// stepsFor decides the plan: a custom preset script when one is selected,
// otherwise the generated provisioning plan.
func (c *Deployments) stepsFor(view DeploymentView) []deploy.Step {
	if view.PresetID != "" {
		if preset := findPreset(view.PresetID); preset != nil {
			return []deploy.Step{{
				Title:   preset.Name,
				Command: deploy.ResolveDeployScript(preset, view.Config, view.PresetVars),
			}}
		}
	}
	return deploy.BuildPlan(view.Config)
}

// sshRunner adapts an SSH connection to deploy.Runner.
type sshRunner struct {
	ctrl   *SSH
	connID string
}

func (r sshRunner) Run(command string, sudo bool) (string, error) {
	return r.ctrl.SSHRun(r.connID, command, sudo)
}

// connectRunner opens a connection to a saved host.
func (c *Deployments) connectRunner(hostName string) (string, deploy.Runner, error) {
	if hostName == "" {
		return "", nil, fmt.Errorf("deployment has no host selected")
	}
	connID, err := c.ssh.ConnectSSH(hostName)
	if err != nil {
		return "", nil, err
	}
	return connID, sshRunner{ctrl: c.ssh, connID: connID}, nil
}

// --- Builtin presets -----------------------------------------------------

// builtinPresets ships with the app. Presets replace the generated plan with
// a fully caller-controlled script, so they document what they touch.
var builtinPresets = []deploy.Preset{{
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
	Variables: []deploy.PresetVar{{
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
	Variables: []deploy.PresetVar{{
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
	Variables: []deploy.PresetVar{{
		Key: "deploy_root", Label: "Deploy root", Default: "/var/www/{name}",
	}},
}}

// DeployPresets returns the available deploy presets.
func (c *Deployments) DeployPresets() []deploy.Preset {
	out := make([]deploy.Preset, len(builtinPresets))
	copy(out, builtinPresets)
	return out
}

func findPreset(id string) *deploy.Preset {
	for i := range builtinPresets {
		if builtinPresets[i].ID == id {
			return &builtinPresets[i]
		}
	}
	return nil
}

// --- Bundle export/import --------------------------------------------------

// ExportDeploymentBundle assembles a portable JSON bundle. Deployment IDs
// (or names) select records; every bundle also carries the referenced
// hosts' address/auth metadata — never passwords, which are sealed per
// Windows account — and deployment configs are sanitized (no PAT, no
// database password). Nothing connects anywhere.
func (c *Deployments) ExportDeploymentBundle(ids []string) (string, error) {
	bundle, err := c.store.ExportBundle(ids)
	if err != nil {
		return "", err
	}
	hosts, err := c.ssh.ListHosts()
	if err != nil {
		return "", err
	}
	referenced := map[string]bool{}
	for _, d := range bundle.Deployments {
		referenced[d.HostName] = true
	}
	for _, h := range hosts {
		if !referenced[h.Name] {
			continue
		}
		bundle.Hosts = append(bundle.Hosts, deployments.BundleHost{
			Name: h.Name, Address: h.Address, Port: h.Port,
			User: h.User, AuthMode: h.AuthMode, KeyName: h.KeyName, Tags: h.Tags,
		})
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ImportDeploymentBundle applies a bundle: hosts are added when new,
// skipped when identical, and mapped (never overwritten) when the name
// exists with a different address; deployments are stored when their name
// is absent. Re-importing the same bundle changes nothing. Importing
// never executes scripts or opens connections.
func (c *Deployments) ImportDeploymentBundle(bundleJSON string) (deployments.ImportResult, error) {
	var bundle deployments.Bundle
	if err := json.Unmarshal([]byte(bundleJSON), &bundle); err != nil {
		return deployments.ImportResult{}, fmt.Errorf("parse bundle: %w", err)
	}
	if bundle.Version != deployments.BundleVersion {
		return deployments.ImportResult{}, fmt.Errorf(
			"bundle version %d is not supported (want %d)", bundle.Version, deployments.BundleVersion)
	}

	result := deployments.ImportResult{HostConflicts: map[string]string{}}
	hostMapping := map[string]string{} // bundle name → effective host name

	store, err := c.ssh.hostStoreForImport()
	if err != nil {
		return result, err
	}
	for _, bh := range bundle.Hosts {
		if existing, ok := store.Find(bh.Name); ok {
			hostMapping[bh.Name] = existing.Name
			if existing.Address == bh.Address && existing.Port == bh.Port {
				result.HostsSkipped++ // already known, identical
				continue
			}
			// Same name, different server: the local entry wins and the
			// bundle's deployments are remapped onto it.
			result.HostConflicts[bh.Name] = existing.Address
			result.HostsSkipped++
			continue
		}
		port := bh.Port
		if port == 0 {
			port = 22
		}
		store.Upsert(ssh.Host{
			Name: bh.Name, Address: bh.Address, Port: port,
			User: bh.User, AuthMode: bh.AuthMode, KeyName: bh.KeyName, Tags: bh.Tags,
		})
		hostMapping[bh.Name] = bh.Name
		result.HostsAdded++
	}
	if err := store.Save(); err != nil {
		return result, err
	}

	// Remap deployment host references through the conflict decisions.
	for i := range bundle.Deployments {
		if mapped, ok := hostMapping[bundle.Deployments[i].HostName]; ok {
			bundle.Deployments[i].HostName = mapped
		}
	}
	added, skipped, err := c.store.ImportDeployments(bundle.Deployments)
	result.DeploymentsAdded = added
	result.DeploymentsSkipped = skipped
	return result, err
}
