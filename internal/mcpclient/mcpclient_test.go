package mcpclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterUnregisterJSONClients(t *testing.T) {
	for _, client := range []Client{ClaudeCode, Cursor} {
		home := t.TempDir()
		spec := ServerSpec{
			Command: `C:\apps\kd\kitodock.exe`,
			Args:    []string{"--mcp-serve"},
			Env:     map[string]string{"KITODOCK_OWNER_URL": "http://127.0.0.1:9", "KITODOCK_OWNER_TOKEN": "tok"},
		}

		if Registered(client, home) {
			t.Fatalf("%s: registered before any config", client)
		}
		if err := Register(client, home, spec); err != nil {
			t.Fatal(err)
		}
		if !Registered(client, home) {
			t.Fatalf("%s: not registered after Register", client)
		}
		doc := readFile(t, configPath(client, home))
		for _, want := range []string{ServerName, "--mcp-serve", "KITODOCK_OWNER_TOKEN"} {
			if !strings.Contains(doc, want) {
				t.Errorf("%s config missing %s:\n%s", client, want, doc)
			}
		}

		// Pre-existing user servers survive registration and removal.
		if err := Register(client, home, spec); err != nil {
			t.Fatal(err)
		}

		if err := Unregister(client, home); err != nil {
			t.Fatal(err)
		}
		if Registered(client, home) {
			t.Fatalf("%s: still registered after Unregister", client)
		}
	}
}

func TestRegisterPreservesExistingServers(t *testing.T) {
	home := t.TempDir()
	path := configPath(Cursor, home)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"mcpServers":{"other":{"command":"other.exe"}}}`), 0o600)

	if err := Register(Cursor, home, ServerSpec{Command: "kitodock.exe"}); err != nil {
		t.Fatal(err)
	}
	doc := readFile(t, path)
	if !strings.Contains(doc, `"other"`) || !strings.Contains(doc, ServerName) {
		t.Fatalf("existing server clobbered:\n%s", doc)
	}
	// The pre-registration state is backed up once.
	bak := readFile(t, path+".kitodock-bak")
	if !strings.Contains(bak, `"other"`) || strings.Contains(bak, ServerName) {
		t.Fatalf("backup wrong: %s", bak)
	}

	if err := Unregister(Cursor, home); err != nil {
		t.Fatal(err)
	}
	// Removal restores the backup: only "other" remains.
	after := readFile(t, path)
	if strings.Contains(after, ServerName) || !strings.Contains(after, `"other"`) {
		t.Fatalf("unregister result:\n%s", after)
	}
}

func TestRegisterUnregisterCodexTOML(t *testing.T) {
	home := t.TempDir()
	path := configPath(CodexCLI, home)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("model = \"gpt-5\"\n"), 0o600)

	spec := ServerSpec{
		Command: "kitodock.exe",
		Args:    []string{"--mcp-serve"},
		Env:     map[string]string{"KITODOCK_OWNER_TOKEN": "tok"},
	}
	if err := Register(CodexCLI, home, spec); err != nil {
		t.Fatal(err)
	}
	doc := readFile(t, path)
	for _, want := range []string{
		"[mcp_servers.kitodock]", `command = "kitodock.exe"`,
		`args = ["--mcp-serve"]`, `env.KITODOCK_OWNER_TOKEN = "tok"`,
		`model = "gpt-5"`,
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("codex config missing %s:\n%s", want, doc)
		}
	}
	if !Registered(CodexCLI, home) {
		t.Fatal("codex not registered")
	}

	// Another TOML section after ours survives removal.
	os.WriteFile(path, []byte(doc+"\n[mcp_servers.other]\ncommand = \"other\"\n"), 0o600)
	if err := removeTOMLSection(path); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, path)
	if strings.Contains(after, "[mcp_servers.kitodock]") {
		t.Fatalf("section not removed:\n%s", after)
	}
	if !strings.Contains(after, "[mcp_servers.other]") || !strings.Contains(after, `model = "gpt-5"`) {
		t.Fatalf("neighbouring config lost:\n%s", after)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
