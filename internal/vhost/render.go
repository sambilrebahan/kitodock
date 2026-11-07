// Package vhost renders per-project web-server configuration for all three
// supported servers at once (switching servers later must not leave stale
// files behind), plus the local root CA, per-host leaf certificates and the
// hosts-file entries a project hostname needs.
package vhost

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Kind is how a project is served.
type Kind string

const (
	// KindPHP serves a docroot through php-cgi pools.
	KindPHP Kind = "php"
	// KindStatic serves files straight from disk.
	KindStatic Kind = "static"
	// KindProxy forwards everything to a dev server on localhost.
	KindProxy Kind = "proxy"
)

// Spec fully describes one project's serving configuration. All three
// renderers consume the same struct, so a change lands everywhere at once.
type Spec struct {
	// Folder is the project's directory-name id ("shop" of shop.test).
	Folder string
	// ServerName is the full hostname ("shop.test").
	ServerName string
	// DocRoot is the absolute document root for PHP/static serving.
	DocRoot string
	// Kind selects the serving mode.
	Kind Kind
	// FastCGIPort is the php-cgi port when Kind is PHP (upstream names
	// resolve through internal/php's scheme).
	FastCGIUpstream string // nginx upstream name; empty → direct port
	FastCGIPort     int
	// ProxyPort is the dev-server port for KindProxy.
	ProxyPort int

	// SSL enables the HTTPS variant; CertFile/KeyFile are produced by the
	// CA module (forward slashes, nginx-style).
	SSL      bool
	CertFile string
	KeyFile  string
}

func (s Spec) nginxName() string  { return s.Folder + ".conf" }
func (s Spec) apacheName() string { return s.Folder + ".conf" }
func (s Spec) caddyName() string  { return s.Folder + ".caddy" }

// Paths returns the per-server config file paths inside their site dirs.
func (s Spec) Paths(nginxSites, apacheSites, caddySites string) (nginx, apache, caddy string) {
	return filepath.Join(nginxSites, s.nginxName()),
		filepath.Join(apacheSites, s.apacheName()),
		filepath.Join(caddySites, s.caddyName())
}

// quoteSlash renders a Windows path the way server configs want it:
// forward slashes, safely quoted.
func quoteSlash(path string) string {
	return `"` + filepath.ToSlash(path) + `"`
}

// --- nginx ----------------------------------------------------------------

func RenderNginx(spec Spec) string {
	var b strings.Builder
	if spec.Kind == KindProxy && spec.ProxyPort > 0 {
		// Dual-stack upstream: Node 17+ binds ::1 first, older bind 127.0.0.1.
		fmt.Fprintf(&b, "upstream %s_dev {\n    server 127.0.0.1:%d;\n    server [::1]:%d backup;\n}\n\n",
			spec.Folder, spec.ProxyPort, spec.ProxyPort)
	}

	writeServer := func(listen string) {
		fmt.Fprintf(&b, "server {\n    listen %s;\n    server_name %s;\n\n", listen, spec.ServerName)
		switch spec.Kind {
		case KindProxy:
			fmt.Fprintf(&b, "    location / {\n        proxy_pass http://%s_dev;\n", spec.Folder)
			b.WriteString("        proxy_http_version 1.1;\n")
			b.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
			b.WriteString("        proxy_set_header Connection \"upgrade\";\n")
			b.WriteString("        proxy_set_header Host $host;\n")
			b.WriteString("        proxy_read_timeout 86400s;\n")
			b.WriteString("    }\n")
		case KindPHP:
			fmt.Fprintf(&b, "    root %s;\n    index index.php index.html;\n\n", quoteSlash(spec.DocRoot))
			b.WriteString("    location / {\n        try_files $uri $uri/ /index.php?$query_string;\n    }\n\n")
			b.WriteString("    location ~ \\.php$ {\n")
			b.WriteString("        include fastcgi_params;\n")
			b.WriteString("        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n")
			if spec.FastCGIUpstream != "" {
				fmt.Fprintf(&b, "        fastcgi_pass %s;\n", spec.FastCGIUpstream)
			} else {
				fmt.Fprintf(&b, "        fastcgi_pass 127.0.0.1:%d;\n", spec.FastCGIPort)
			}
			b.WriteString("    }\n")
		default:
			fmt.Fprintf(&b, "    root %s;\n    index index.html index.htm;\n\n", quoteSlash(spec.DocRoot))
			b.WriteString("    location / {\n        try_files $uri $uri/ =404;\n    }\n")
		}
		if spec.SSL && strings.HasPrefix(listen, "443") {
			fmt.Fprintf(&b, "\n    ssl_certificate %s;\n    ssl_certificate_key %s;\n",
				quoteSlash(spec.CertFile), quoteSlash(spec.KeyFile))
		}
		b.WriteString("}\n\n")
	}

	if spec.SSL {
		writeServer("443 ssl")
	}
	writeServer("80")
	return b.String()
}

// --- Apache ---------------------------------------------------------------

func RenderApache(spec Spec) string {
	var b strings.Builder
	writeVhost := func(port string) {
		fmt.Fprintf(&b, "<VirtualHost *:%s>\n", port)
		fmt.Fprintf(&b, "    ServerName %s\n", spec.ServerName)
		switch spec.Kind {
		case KindProxy:
			fmt.Fprintf(&b, "    RewriteEngine On\n    RewriteRule ^/(.*)$ http://localhost:%d/$1 [P,L]\n", spec.ProxyPort)
		case KindPHP:
			fmt.Fprintf(&b, "    DocumentRoot %s\n", quoteSlash(spec.DocRoot))
			b.WriteString("    <Directory " + quoteSlash(spec.DocRoot) + ">\n")
			b.WriteString("        AllowOverride All\n        Require all granted\n    </Directory>\n")
			b.WriteString("    FallbackResource /index.php\n")
			fmt.Fprintf(&b, "    <FilesMatch \\.php$>\n        SetHandler \"proxy:fcgi://127.0.0.1:%d\"\n    </FilesMatch>\n",
				spec.FastCGIPort)
		default:
			fmt.Fprintf(&b, "    DocumentRoot %s\n", quoteSlash(spec.DocRoot))
			b.WriteString("    <Directory " + quoteSlash(spec.DocRoot) + ">\n        Require all granted\n    </Directory>\n")
		}
		if spec.SSL && port == "443" {
			fmt.Fprintf(&b, "    SSLEngine on\n    SSLCertificateFile %s\n    SSLCertificateKeyFile %s\n",
				quoteSlash(spec.CertFile), quoteSlash(spec.KeyFile))
		}
		b.WriteString("</VirtualHost>\n")
	}
	if spec.SSL {
		writeVhost("443")
	}
	writeVhost("80")
	return b.String()
}

// --- Caddy ----------------------------------------------------------------

func RenderCaddy(spec Spec) string {
	var b strings.Builder
	host := spec.ServerName
	if spec.SSL {
		fmt.Fprintf(&b, "%s {\n    tls %s %s\n", host, spec.CertFile, spec.KeyFile)
	} else {
		// Explicit http:// disables Caddy's automatic ACME for .test.
		fmt.Fprintf(&b, "http://%s {\n", host)
	}
	switch spec.Kind {
	case KindProxy:
		fmt.Fprintf(&b, "    reverse_proxy 127.0.0.1:%d [::1]:%d\n", spec.ProxyPort, spec.ProxyPort)
	case KindPHP:
		fmt.Fprintf(&b, "    root %s\n    php_fastcgi 127.0.0.1:%d\n    file_server\n", spec.DocRoot, spec.FastCGIPort)
	default:
		fmt.Fprintf(&b, "    root %s\n    file_server\n", spec.DocRoot)
	}
	b.WriteString("}\n")
	return b.String()
}
