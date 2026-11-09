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
