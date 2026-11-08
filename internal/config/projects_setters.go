package config

// Project-preference setters, separated from the document types so
// config.go stays a schema description.

// SetProjectSSL persists one project's HTTPS preference.
func (s *Store) SetProjectSSL(name string, enabled bool) error {
	return s.commit(func(c *Config) {
		if c.Projects.SSL == nil {
			c.Projects.SSL = map[string]bool{}
		}
		if enabled {
			c.Projects.SSL[name] = true
		} else {
			delete(c.Projects.SSL, name)
		}
	})
}

// SetProjectPhp persists a project's PHP series override; "" clears it.
func (s *Store) SetProjectPhp(name, series string) error {
	return s.commit(func(c *Config) {
		if c.Projects.PhpVersions == nil {
			c.Projects.PhpVersions = map[string]string{}
		}
		if series == "" {
			delete(c.Projects.PhpVersions, name)
		} else {
			c.Projects.PhpVersions[name] = series
		}
	})
}

// SetProjectsTLD persists the projects TLD.
func (s *Store) SetProjectsTLD(tld string) error {
	return s.commit(func(c *Config) { c.Projects.TLD = tld })
}

// SetCrons persists the cron job list (raw JSON to avoid a cron import).
func (s *Store) SetCrons(jobs []map[string]any) error {
	return s.commit(func(c *Config) { c.Crons = jobs })
}
