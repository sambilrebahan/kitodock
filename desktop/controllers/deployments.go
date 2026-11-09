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
func (c *Deployments) SaveDeployment(view DeploymentView) error {
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
// touching the server. Custom presets resolve to their script step.
func (c *Deployments) DeployPlanPreview(view DeploymentView) []deploy.Step {
	return c.stepsFor(view)
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
// Progress streams as deploy:step / deploy:line; the finish is deploy:done.
func (c *Deployments) StartApply(id string) (string, error) {
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
	if err := c.store.StartRun(id, runID, deployments.KindApply); err != nil {
		release()
		return "", err
	}

	go func() {
		defer release()
		status, errMsg := "success", ""
		output, err := c.applyOnce(id, runID, view)
		if err != nil {
			status, errMsg = "error", err.Error()
		}
		_ = c.store.FinishRun(id, runID, status, errMsg)
		_ = c.store.SaveRunLog(id, runID, output+errMsg)
		c.bus.Emit(EvDeployDone, map[string]any{
			"deploymentId": id, "runId": runID, "status": status, "error": errMsg,
		})
	}()
	return runID, nil
}

// applyOnce performs one synchronous apply, streaming events as it goes.
func (c *Deployments) applyOnce(id, runID string, view DeploymentView) (string, error) {
	connID, runner, err := c.connectRunner(view.HostName)
	if err != nil {
		return "", err
	}
	defer c.ssh.DisconnectSSH(connID)

	onStep := func(stepIndex int, title, status string) {
		c.bus.Emit(EvDeployStep, map[string]any{
			"deploymentId": id, "runId": runID,
			"index": stepIndex, "title": title, "status": status,
		})
	}
	onLine := func(line string) {
		c.bus.Emit(EvDeployLine, map[string]any{
			"deploymentId": id, "runId": runID, "line": line,
		})
	}
	return deploy.RunApply(runner, c.stepsFor(view), onStep, onLine)
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
