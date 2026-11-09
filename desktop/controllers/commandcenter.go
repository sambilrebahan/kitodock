package controllers

// The command center controller: the palette (Ctrl+K) asks for the
// command catalog and executes intents. Commands are grouped, carry a
// scope hint for filtering, and run through the owning controllers; the
// palette itself lives entirely in the frontend.

import (
	"fmt"
	"sort"
	"strings"
)

// Command is one palette entry.
type Command struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Group string `json:"group"`
	// Hint carries dynamic context (project name, host…) shown after the
	// title.
	Hint string `json:"hint,omitempty"`
	// Keywords feed the fuzzy filter.
	Keywords []string `json:"keywords,omitempty"`
}

// CommandContext lists dynamic targets the palette offers.
type CommandContext struct {
	Pages    []string `json:"pages"`
	Projects []string `json:"projects"`
	Services []string `json:"services"`
	Hosts    []string `json:"hosts"`
	Agents   []string `json:"agents"`
}

// CommandCenter builds catalogs and executes intents.
type CommandCenter struct {
	bus     *EventBus
	context func() CommandContext
	// navigate asks the UI to switch page/project.
	navigate func(target string)
	// runners hold intent executors by prefix: page:, project:, service:,
	// terminal:, ssh:, agent:.
	runners map[string]func(arg string) error
}

// NewCommandCenter wires the dispatch table.
func NewCommandCenter(bus *EventBus, context func() CommandContext, navigate func(string),
	runners map[string]func(arg string) error) *CommandCenter {
	return &CommandCenter{bus: bus, context: context, navigate: navigate, runners: runners}
}

// CommandCatalog returns every available command.
func (c *CommandCenter) CommandCatalog() []Command {
	ctx := c.context()
	var out []Command
	for _, page := range ctx.Pages {
		out = append(out, Command{
			ID: "page:" + page, Title: "Go to " + page, Group: "navigate",
			Keywords: []string{"open", page},
		})
	}
	for _, project := range ctx.Projects {
		out = append(out, Command{
			ID: "project:" + project, Title: "Open project", Hint: project,
			Group: "projects", Keywords: []string{"project", project},
		})
		out = append(out, Command{
			ID: "terminal:" + project, Title: "New terminal in", Hint: project,
			Group: "terminal", Keywords: []string{"terminal", "shell", project},
		})
	}
	for _, service := range ctx.Services {
		out = append(out, Command{
			ID: "service:" + service + ":start", Title: "Start service", Hint: service,
			Group: "services", Keywords: []string{"service", "start", service},
		})
		out = append(out, Command{
			ID: "service:" + service + ":stop", Title: "Stop service", Hint: service,
			Group: "services", Keywords: []string{"service", "stop", service},
		})
	}
	for _, host := range ctx.Hosts {
		out = append(out, Command{
			ID: "ssh:" + host, Title: "SSH shell", Hint: host,
			Group: "ssh", Keywords: []string{"ssh", "remote", host},
		})
	}
	for _, agent := range ctx.Agents {
		out = append(out, Command{
			ID: "agent:" + agent, Title: "Launch agent", Hint: agent,
			Group: "agents", Keywords: []string{"agent", agent},
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// RunCommand executes one catalog id. Unknown intents error; the navigate
// prefix also emits so keep-alive pages can react.
func (c *CommandCenter) RunCommand(id string) error {
	prefix, arg, found := strings.Cut(id, ":")
	if !found {
		return fmt.Errorf("malformed command %q", id)
	}
	if prefix == "page" {
		c.navigate("page:" + arg)
		return nil
	}
	if prefix == "project" {
		c.navigate("project:" + arg)
		return nil
	}
	// service:<name>:<action> carries two segments after the prefix.
	if prefix == "service" {
		name, action, ok := strings.Cut(arg, ":")
		if !ok {
			return fmt.Errorf("malformed service command %q", id)
		}
		runner, ok := c.runners["service:"+action]
		if !ok {
			return fmt.Errorf("unknown service action %q", action)
		}
		return runner(name)
	}
	runner, ok := c.runners[prefix]
	if !ok {
		return fmt.Errorf("unknown command %q", id)
	}
	return runner(arg)
}
