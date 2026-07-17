package provider

import (
	"fmt"
	"html"
	"path/filepath"
	"strings"

	"github.com/chokunplayz/poormanwebctrl/internal/config"
	"github.com/chokunplayz/poormanwebctrl/internal/plan"
	"github.com/chokunplayz/poormanwebctrl/internal/platform"
)

func addSites(pn *plan.Plan, c config.Config, p platform.Platform) {
	service := webServiceName(c.WebServer.Provider, p)
	writableWordPress := anyWordPress(c) && wordpressInitializationAllowed(c)
	if writableWordPress {
		download := plan.Cmd("Download wp-cli", "curl", true, "-fsSL", "-o", "/tmp/poorman-wp-cli.phar", "https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar")
		download.UnlessCommand, download.UnlessArgs = "wp", []string{"--info"}
		install := plan.Cmd("Install wp-cli", "install", true, "-m", "0755", "/tmp/poorman-wp-cli.phar", "/usr/local/bin/wp")
		install.UnlessCommand, install.UnlessArgs = "wp", []string{"--info"}
		pn.Add(download, install)
	}
	if c.WebServer.Provider == "openlitespeed" {
		pn.Add(plan.EnsureLine("Enable poorman OpenLiteSpeed includes", "/usr/local/lsws/conf/httpd_config.conf", "include /usr/local/lsws/conf/poorman.conf"))
		pn.Add(plan.ManagedFile("Register OpenLiteSpeed virtual hosts and listener", "/usr/local/lsws/conf/poorman.conf", openLiteSpeedServerConfig(c), "root", 0o600))
	}
	if c.WebServer.Provider == "nginx" && p.Family == "alpine" && hasPHPSite(c) {
		pool := "[www]\nuser = nginx\ngroup = nginx\nlisten = 127.0.0.1:9000\npm = dynamic\npm.max_children = 10\npm.start_servers = 2\npm.min_spare_servers = 1\npm.max_spare_servers = 3\n"
		pn.Add(plan.ManagedFile("Configure Alpine PHP-FPM pool", "/etc/php84/php-fpm.d/zz-poorman.conf", pool, "root", 0o644))
	}
	for _, s := range c.Sites {
		_, runtimeGroup := webRuntimeIdentity(c.WebServer.Provider, p)
		owner := s.Owner
		if owner == "" {
			owner, _ = webRuntimeIdentity(c.WebServer.Provider, p)
		}
		pn.Add(plan.DirOwnedBy("Create document root for "+s.Domain, s.Root, owner, runtimeGroup, 0o750))
		if s.WordPress == nil {
			indexPath := filepath.Join(s.Root, "index.html")
			pn.Add(plan.FileIfDirectoryEmptyOwnedBy("Create welcome page for "+s.Domain, indexPath, welcomePage(c.WebServer.Provider, s, indexPath), owner, runtimeGroup, 0o640))
		}
		path, content := siteConfig(c.WebServer.Provider, s, p)
		pn.Add(plan.ManagedFile("Configure virtual host "+s.Domain, path, content, "root", 0o644))
		if s.WordPress != nil && writableWordPress {
			pn.Add(plan.AsUser("Download WordPress for "+s.Domain, owner, "wp", "core", "download", "--path="+s.Root))
			if c.Database != nil {
				name, user, passwordEnv := c.Database.ApplicationCredentials()
				step := plan.AsUser("Create WordPress configuration for "+s.Domain, owner, "wp", "config", "create", "--path="+s.Root, "--dbname="+name, "--dbuser="+user, "--prompt=dbpass")
				step.Input, step.Sensitive = "${"+passwordEnv+"}\n", true
				pn.Add(step)
			}
			wp := s.WordPress
			scheme := "http"
			if c.SiteTLSEnabled(s) {
				scheme = "https"
			}
			step := plan.AsUser("Install WordPress for "+s.Domain, owner, "wp", "core", "install", "--path="+s.Root, "--url="+scheme+"://"+s.Domain, "--title="+defaultString(wp.Title, s.Domain), "--admin_user="+defaultString(wp.AdminUser, "admin"), "--admin_email="+wp.AdminEmail, "--prompt=admin_password")
			step.Input, step.Sensitive = "${"+wp.AdminPassEnv+"}\n", true
			pn.Add(step)
		}
	}
	if anyWordPress(c) && !writableWordPress {
		pn.Warn("Skipped WordPress initialization because this database is a replica or promoted independent instance")
	}
	if c.WebServer.Provider == "openlitespeed" {
		exampleUser, exampleGroup := openLiteSpeedRuntimeIdentity(p)
		pn.Add(plan.DirOwnedBy("Restore OpenLiteSpeed example root ownership", "/usr/local/lsws/Example/html", exampleUser, exampleGroup, 0o755))
	}
	pn.Add(plan.Cmd("Validate "+c.WebServer.Provider+" configuration", validationCommand(c.WebServer.Provider), true, validationArgs(c.WebServer.Provider)...))
	pn.Add(restartService(p, service))
	if c.WebServer.Provider == "openlitespeed" {
		pn.Warn("OpenLiteSpeed include-managed configuration is edited as files and will not appear as editable state in WebAdmin")
	}
	if hasPHPSite(c) && c.WebServer.Provider == "nginx" {
		if p.Family == "debian" {
			pn.Warn("Verify the distro's versioned PHP-FPM service and /run/php/php-fpm.sock compatibility after installation")
		} else {
			phpService := "php-fpm"
			if p.Family == "alpine" {
				phpService = "php-fpm84"
			}
			pn.Add(enableService(p, phpService))
		}
	}
}

func welcomePage(web string, site config.Site, indexPath string) string {
	runtime := "static HTML"
	if site.Runtime == "php" || site.WordPress != nil {
		runtime = "PHP"
	}
	stack := web + " + " + runtime
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Welcome to %s</title>
  <style>
    body { max-width: 42rem; margin: 10vh auto; padding: 2rem; font: 18px/1.6 system-ui, sans-serif; color: #222; }
    code { overflow-wrap: anywhere; }
  </style>
</head>
<body>
  <h1>Welcome to %s</h1>
  <p>this website is setup using Poorman's Panel<br>with %s</p>
  <p>to replace this page, replace the index file in<br><code>%s</code></p>
</body>
</html>
`, html.EscapeString(site.Domain), html.EscapeString(site.Domain), html.EscapeString(stack), html.EscapeString(indexPath))
}

func openLiteSpeedRuntimeIdentity(p platform.Platform) (string, string) {
	if p.Family == "debian" {
		return "nobody", "nogroup"
	}
	return "nobody", "nobody"
}

func webRuntimeIdentity(web string, p platform.Platform) (string, string) {
	switch web {
	case "openlitespeed":
		return openLiteSpeedRuntimeIdentity(p)
	case "apache":
		if p.Family == "debian" {
			return "www-data", "www-data"
		}
		return "apache", "apache"
	default:
		if p.Family == "debian" {
			return "www-data", "www-data"
		}
		return "nginx", "nginx"
	}
}

func siteConfig(web string, s config.Site, p platform.Platform) (string, string) {
	aliases := strings.Join(s.Aliases, " ")
	if web == "nginx" {
		php := "\n    location / { try_files $uri $uri/ =404; }"
		if s.Runtime == "php" {
			socket := "/run/php/php-fpm.sock"
			if p.Family == "rhel" {
				socket = "/run/php-fpm/www.sock"
			} else if p.Family == "alpine" {
				socket = "127.0.0.1:9000"
			}
			upstream := "unix:" + socket
			if p.Family == "alpine" {
				upstream = socket
			}
			php = fmt.Sprintf("\n    index index.php index.html;\n    location / { try_files $uri $uri/ /index.php?$args; }\n    location ~ \\.php$ { include fastcgi_params; fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name; fastcgi_pass %s; }", upstream)
		}
		content := fmt.Sprintf("%sserver {\n    listen 80;\n    server_name %s %s;\n    root %s;%s\n}\n", managedConfigHeader, s.Domain, aliases, s.Root, php)
		return "/etc/nginx/conf.d/poorman-" + s.Domain + ".conf", content
	}
	if web == "apache" {
		content := fmt.Sprintf("%s<VirtualHost *:80>\n  ServerName %s\n  ServerAlias %s\n  DocumentRoot %s\n  <Directory %s>\n    AllowOverride All\n    Require all granted\n  </Directory>\n</VirtualHost>\n", managedConfigHeader, s.Domain, aliases, s.Root, s.Root)
		base := "/etc/httpd/conf.d"
		if p.Family == "debian" {
			base = "/etc/apache2/sites-enabled"
		}
		return filepath.Join(base, "poorman-"+s.Domain+".conf"), content
	}
	owner := s.Owner
	if owner == "" {
		owner, _ = openLiteSpeedRuntimeIdentity(p)
	}
	content := fmt.Sprintf("%sdocRoot                   %s\nvhDomain                  %s\nvhAliases                 %s\nenableGzip                1\nindex  {\n  useServer               0\n  indexFiles              index.php,index.html\n}\nrewrite  {\n  enable                  1\n  autoLoadHtaccess        1\n}\nextprocessor lsphp {\n  type                    lsapi\n  address                 uds://tmp/lshttpd/%s.sock\n  maxConns                10\n  env                     LSAPI_CHILDREN=10\n  initTimeout             60\n  retryTimeout            0\n  persistConn             1\n  respBuffer              0\n  autoStart               1\n  path                    /usr/local/lsws/lsphp84/bin/lsphp\n  backlog                 100\n  instances               1\n  extUser                 %s\n  extGroup                %s\n}\nscriptHandler  {\n  add                     lsapi:lsphp php\n}\n", managedConfigHeader, s.Root, s.Domain, strings.Join(s.Aliases, ","), s.Domain, owner, owner)
	return "/usr/local/lsws/conf/vhosts/" + s.Domain + "/vhconf.conf", content
}

func openLiteSpeedServerConfig(c config.Config) string {
	var b strings.Builder
	b.WriteString(managedConfigHeader)
	b.WriteString("listener poormanHTTP {\n  address                 *:80\n  secure                  0\n")
	for _, s := range c.Sites {
		domains := append([]string{s.Domain}, s.Aliases...)
		fmt.Fprintf(&b, "  map                     %s %s\n", s.Domain, strings.Join(domains, ","))
	}
	b.WriteString("}\n")
	for _, s := range c.Sites {
		fmt.Fprintf(&b, "virtualhost %s {\n  vhRoot                  %s\n  configFile              conf/vhosts/%s/vhconf.conf\n  allowSymbolLink         1\n  enableScript            1\n  restrained              1\n}\n", s.Domain, s.Root, s.Domain)
	}
	return b.String()
}

func addFTP(pn *plan.Plan, c config.Config, p platform.Platform) {
	if !c.Access.FTP.Enabled {
		return
	}
	conf := "# Managed by poorman\nlisten=YES\nanonymous_enable=NO\nlocal_enable=YES\nwrite_enable=YES\nchroot_local_user=YES\nallow_writeable_chroot=YES\n"
	pn.Add(plan.ManagedFile("Configure explicitly enabled plaintext FTP", "/etc/vsftpd.conf", conf, "root", 0o600), enableService(p, "vsftpd"))
	pn.Warn("Plain FTP exposes credentials and data; migrate clients to SFTP")
}
