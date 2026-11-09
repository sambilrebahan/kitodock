package deploy

// Secret redaction: anything the plan splices into commands (PATs, db
// passwords) must never surface verbatim in streamed output, stored run
// logs, or step previews. The redactor is applied by whoever surfaces
// output — the executor callbacks and the controller wrapping them.

import (
	"fmt"
	"regexp"
	"strings"
)

// Redactor scrubs known secrets from text.
type Redactor struct {
	// literals are exact secrets to mask wherever they appear.
	literals []string
	// credentialURL matches https://<credentials>@host URLs and masks the
	// credential part, covering PAT shapes not known in advance.
	credentialURL *regexp.Regexp
}

// urlCredentialRe captures the user:password (or bare token) segment of an
// HTTPS URL, so echoed clone URLs never leak what was spliced into them.
var urlCredentialRe = regexp.MustCompile(`(https://)[^/@:\s]+(:[^/@\s]+)?@`)

// NewRedactor builds a redactor for the secrets embedded in one config.
// Empty secrets are ignored.
func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{credentialURL: urlCredentialRe}
	for _, s := range secrets {
		if len(strings.TrimSpace(s)) >= 6 { // too short = noisy masking
			r.literals = append(r.literals, s)
		}
	}
	return r
}

// RedactorFor returns a redactor covering the PAT and database password a
// config splices into its generated commands.
func RedactorFor(cfg ApplyConfig) *Redactor {
	return NewRedactor(cfg.GitPAT, cfg.Database.Password)
}

// Redact masks every known secret and URL credential in text.
func (r *Redactor) Redact(text string) string {
	if r == nil {
		return text
	}
	out := text
	for _, secret := range r.literals {
		if secret == "" {
			continue
		}
		out = strings.ReplaceAll(out, secret, "*****")
	}
	out = r.credentialURL.ReplaceAllString(out, "${1}*****@")
	return out
}

// RedactSteps returns a copy of steps safe for display: commands carry
// spliced secrets, so every command is run through the redactor.
func (r *Redactor) RedactSteps(steps []Step) []Step {
	if r == nil {
		return steps
	}
	out := make([]Step, len(steps))
	copy(out, steps)
	for i := range out {
		out[i].Command = r.Redact(out[i].Command)
	}
	return out
}

// String implements fmt.Stringer with redaction always applied, so
// accidental %s/%v logging of a Redactor never prints secrets.
func (r *Redactor) String() string {
	return fmt.Sprintf("Redactor(%d literals)", len(r.literals))
}
