// Package projectroutes persists which hostname serves which checkout:
// the canonical domain (myapp.test) and optional preview domains for
// worktrees (feature.myapp.test). The store is a single JSON file; the
// manager validates every mutation and hands the caller a single atomic
// "apply" callback that does the actual vhost/hosts/cert work.
package projectroutes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"kitodock/internal/atomicfile"
)

// Route is one hostname → target mapping.
type Route struct {
	ID           string    `json:"id"`
	ProjectName  string    `json:"projectName"`
	Hostname     string    `json:"hostname"`
	Canonical    bool      `json:"canonical"`
	TargetKind   string    `json:"targetKind"` // "main-checkout" | "worktree"
	TargetPath   string    `json:"targetPath,omitempty"`
	WorktreePath string    `json:"worktreePath,omitempty"`
	ServeMode    string    `json:"serveMode"` // "docroot" | "proxy"
	ProxyPort    int       `json:"proxyPort,omitempty"`
	SSL          bool      `json:"ssl"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Project groups a project's routes.
type Project struct {
	Name          string
	MainPath      string
	TLD           string
	SSL           bool
	MainProxyPort int
}

// Target resolves a route to a path.
type Target struct {
	Path string
	Kind string
}

const storeVersion = 1

type storeFile struct {
	Version int     `json:"version"`
	Routes  []Route `json:"routes"`
}

// Store is the persisted route table.
type Store struct {
	mu     sync.Mutex
	path   string
	routes map[string]Route // keyed by ID
}

// Notice reports a corrupt store that was preserved aside.
type Notice struct {
	Message       string
	PreservedPath string
}

// OpenStore loads (or starts) the route table at path.
func OpenStore(path string) (*Store, *Notice, error) {
	s := &Store{path: path, routes: map[string]Route{}}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var doc storeFile
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != storeVersion {
		preserved := path + ".unreadable-" + time.Now().UTC().Format("20060102T150405Z")
		_ = os.Rename(path, preserved)
		return s, &Notice{
			Message:       "route table unreadable; preserved and restarted",
			PreservedPath: preserved,
		}, nil
	}
	for _, route := range doc.Routes {
		s.routes[route.ID] = route
	}
	return s, nil, nil
}

// canonicalID is the stable ID of a project's canonical route.
func canonicalID(project string) string { return "canonical:" + project }

// hostnameLabel validates one hostname label (RFC 1123, lowercased).
var hostnameLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// List returns every project's routes, materializing the default canonical
// route for projects that have none yet (fresh scan).
func (s *Store) List(project Project) []Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Route{}
	if _, ok := s.routes[canonicalID(project.Name)]; !ok {
		out = append(out, Route{
			ID: canonicalID(project.Name), ProjectName: project.Name,
			Hostname: project.Name + "." + project.TLD, Canonical: true,
			TargetKind: "main-checkout", TargetPath: project.MainPath,
			ServeMode: "docroot", SSL: project.SSL, UpdatedAt: time.Now(),
		})
	}
	for _, route := range s.routes {
		if route.ProjectName == project.Name {
			out = append(out, route)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Canonical != out[j].Canonical {
			return out[i].Canonical
		}
		return out[i].Hostname < out[j].Hostname
	})
	return out
}

// SetCanonical redirects the canonical hostname to a worktree checkout.
func (s *Store) SetCanonical(project Project, worktreePath string) error {
	return s.commit(project, func(routes map[string]Route) {
		id := canonicalID(project.Name)
		route := routes[id]
		route.ID = id
		route.ProjectName = project.Name
		route.Hostname = project.Name + "." + project.TLD
		route.Canonical = true
		route.TargetKind = "worktree"
		route.TargetPath = worktreePath
		route.SSL = project.SSL
		route.UpdatedAt = time.Now()
		routes[id] = route
	})
}

// RestoreCanonical points the canonical hostname back at the main checkout.
func (s *Store) RestoreCanonical(project Project) error {
	return s.commit(project, func(routes map[string]Route) {
		id := canonicalID(project.Name)
		route := routes[id]
		route.TargetKind = "main-checkout"
		route.TargetPath = project.MainPath
		route.UpdatedAt = time.Now()
		routes[id] = route
	})
}

// CreatePreview adds a worktree preview route (label.label.tld form).
func (s *Store) CreatePreview(project Project, label, worktreePath string, proxyPort int) (Route, error) {
	label = strings.ToLower(strings.TrimSpace(label))
	if !hostnameLabel.MatchString(label) {
		return Route{}, fmt.Errorf("preview label %q is not a valid hostname label", label)
	}
	for _, reserved := range []string{"www", "localhost", "main"} {
		if label == reserved {
			return Route{}, fmt.Errorf("preview label %q is reserved", label)
		}
	}
	hostname := label + "." + project.Name + "." + project.TLD
	if len(hostname) > 253 {
		return Route{}, fmt.Errorf("hostname %q exceeds 253 characters", hostname)
	}
	// No IPs as hostnames.
	if strings.HasPrefix(hostname, "127.") || strings.HasPrefix(hostname, "192.168.") {
		return Route{}, fmt.Errorf("hostname %q looks like an address", hostname)
	}

	id := "preview:" + project.Name + ":" + label
	var createErr error
	var created Route
	err := s.commit(project, func(routes map[string]Route) {
		for _, existing := range routes {
			if strings.EqualFold(existing.Hostname, hostname) {
				createErr = fmt.Errorf("hostname %q is already routed", hostname)
				return
			}
		}
		serveMode := "docroot"
		if proxyPort > 0 {
			serveMode = "proxy"
		}
		created = Route{
			ID: id, ProjectName: project.Name, Hostname: hostname,
			TargetKind: "worktree", TargetPath: worktreePath,
			WorktreePath: worktreePath, ServeMode: serveMode,
			ProxyPort: proxyPort, SSL: project.SSL, UpdatedAt: time.Now(),
		}
		routes[id] = created
	})
	if createErr != nil {
		return Route{}, createErr
	}
	return created, err
}

// Remove drops a non-canonical route.
func (s *Store) Remove(projectName, routeID string) error {
	s.mu.Lock()
	route, ok := s.routes[routeID]
	s.mu.Unlock()
	if !ok || route.ProjectName != projectName {
		return fmt.Errorf("route %s not found", routeID)
	}
	if route.Canonical {
		return fmt.Errorf("the canonical route cannot be removed; restore it instead")
	}
	s.mu.Lock()
	delete(s.routes, routeID)
	s.mu.Unlock()
	return s.persist()
}

// MigrateTLD rewrites every stored hostname from oldTLD to newTLD.
func (s *Store) MigrateTLD(oldTLD, newTLD string) error {
	s.mu.Lock()
	for id, route := range s.routes {
		if strings.HasSuffix(route.Hostname, "."+oldTLD) {
			route.Hostname = strings.TrimSuffix(route.Hostname, "."+oldTLD) + "." + newTLD
			route.UpdatedAt = time.Now()
			s.routes[id] = route
		}
	}
	s.mu.Unlock()
	return s.persist()
}

// commit runs a mutation and persists atomically.
func (s *Store) commit(_ Project, mutate func(map[string]Route)) error {
	s.mu.Lock()
	mutate(s.routes)
	s.mu.Unlock()
	return s.persist()
}

func (s *Store) persist() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := storeFile{Version: storeVersion, Routes: make([]Route, 0, len(s.routes))}
	for _, route := range s.routes {
		out.Routes = append(out.Routes, route)
	}
	sort.Slice(out.Routes, func(i, j int) bool { return out.Routes[i].ID < out.Routes[j].ID })
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, append(data, '\n'), 0o644)
}
