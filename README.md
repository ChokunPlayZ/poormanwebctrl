# poormanwebctrl

`poorman` is a dependency-light Go CLI and guided terminal UI for provisioning and operating a self-hosted web server. Every change is generated as a readable plan before it runs.

## Included workflows

- Nginx, Apache, or OpenLiteSpeed package/service setup
- Static and PHP virtual hosts
- Multiple virtual hosts with aliases, plus guided add/edit/remove management
- PostgreSQL or MariaDB databases and application users
- PostgreSQL streaming-replica and MariaDB GTID primary/replica plans
- Replication status and guarded replica promotion
- Linux users, SSH keys, and SFTP-only groups
- Explicit opt-in vsftpd support (SFTP is the safe default)
- WordPress download, configuration, and installation with wp-cli
- Let's Encrypt certificates for Nginx and Apache
- UFW/firewalld rules, including CIDR-scoped database replication
- Scheduled site/database backups and on-demand backups
- Local service, configuration, and virtual-host health checks
- TUI firewall management: status, policy preview/apply, and guarded disable
- TUI long-term operations: host resource stats, service logs, and backup inventory

## Build and start

```sh
go build -o bin/poorman ./cmd/poorman
./bin/poorman tui
./bin/poorman plan
```

### Install or update

Install the latest release (rerunning the same command updates it):

```sh
curl -fsSL https://raw.githubusercontent.com/ChokunPlayZ/poormanwebctrl/main/install.sh | sh
```

The installer places `poorman` in `~/.local/bin`. Set `POORMAN_INSTALL_DIR` to choose another location, or `POORMAN_VERSION` to install a specific release.

Provisioning is Linux-only. Ubuntu/Debian, RHEL-family Linux, and Alpine are modeled. Development and tests work on macOS.

Press `Ctrl-C` to stop an in-progress operation safely. Completed steps are kept, the active command is canceled, and remaining steps are not started; rerun `apply` to continue from the idempotent plan.

## Main commands

```text
poorman tui                         guided configuration
poorman init                        write a complete starter configuration
poorman plan [-f FILE]              preview every action
poorman apply [-f FILE] [--yes]     apply locally
poorman status [-f FILE]            run health checks
poorman backup [-f FILE] [--yes]    run that configuration's backup job now
poorman replica status [-f FILE]    show replication health/lag data
poorman replica promote [-f FILE]   promote a configured replica
poorman replica setup [-f FILE] [--from PRIMARY_FILE]
                                      guided replica configuration
```

When a configuration already exists, `poorman tui` opens the operations dashboard. Choose **long-term operations** for read-only host capacity, recent systemd journal logs for configured services, and the files currently present in the configured backup destination.

Each successful apply records poorman-managed services in `/var/lib/poorman/managed.json`. This lets the dashboard show database instances from multiple configuration files on the same host and lets a later apply retire an old managed replica service when its port, data directory, or provider changes. Existing data directories are retained.

Choose **Virtual hosts** in that dashboard to list, add, edit, or remove domains. Every host is planned independently, including its document root, aliases, runtime, WordPress setup, and managed server configuration.

Choose **Stack settings** to adjust the web server, database and replication role, TLS email/enabled state, firewall, and backup destination/schedule after setup.

During a new guided setup, the database section asks for `standalone`, `primary`, or `replica`. Primary setup collects the replication network and credentials; replica setup collects the primary host, ports, replication credentials, and the PostgreSQL data directory or MariaDB node ID. Use `poorman replica setup` (or **guided replica setup** in the dashboard) to configure a replica on an existing stack. When `--from` points to a standalone stack, the wizard now saves both sides: it promotes the source file to `primary` with matching replication credentials and writes the independent `replica` file.

## Secrets

Configuration stores environment-variable names, never passwords. Export the values before `apply`:

```sh
export POORMAN_DB_PASSWORD='use-a-password-manager-generated-value'
export POORMAN_REPLICATION_PASSWORD='a-separate-replication-password'
export POORMAN_WP_ADMIN_PASSWORD='another-unique-value'
./bin/poorman apply
```

Secret-backed command input is redacted from plans. SQL secrets are escaped before being sent over standard input rather than included in process arguments.

## Replication

Create one configuration per database node. Set `database.role` to `primary` or `replica`, give every MariaDB node a unique `node_id`, and restrict the primary with `allowed_cidr`. See [PostgreSQL primary](examples/postgresql-primary.json) and [PostgreSQL replica](examples/postgresql-replica.json).

For a primary and replica on the same machine, create a separate replica file with `poorman replica setup -f replica.json --from primary.json`, then answer yes when guided setup asks whether the primary is local. PostgreSQL uses separate ports and a dedicated data directory. MariaDB on Debian/Ubuntu and RHEL-family systems gets its own data directory, config, socket, PID, log, port, systemd service, seed snapshot, and instance-specific backup job; see [the same-host MariaDB replica example](examples/mariadb-replica-same-host.json). Apply the primary configuration first so its replication account and binlogs exist, then apply the replica configuration.

The two database processes are deliberately not coupled with a systemd `Requires=` or `PartOf=` relationship, so one database service crashing does not stop the other. Same-host replication is process-level redundancy only: the primary and replica still share the machine, storage hardware, kernel, and power source. Use a separate host for host-level availability. Same-host MariaDB service generation currently requires systemd; Alpine/OpenRC needs a separate replica host.

Promotion is deliberately guarded. Before running it, fence the failed primary to prevent split brain. `poorman replica promote` requires typing `PROMOTE` unless `--yes` is supplied.

For a promoted same-host MariaDB instance, changing `database.role` to `primary` keeps poorman attached to the independent service and makes its managed configuration writable. Client redirection is still explicit; local applications must be pointed at the promoted instance's configured port.

PostgreSQL still requires the precise `pg_hba.conf` rule printed in the plan. The tool does not guess trusted networks or edit a version-dependent authentication file blindly.

## Operational cautions

- Review `plan` on the actual target OS before applying.
- Test backups by restoring them to a separate host.
- OpenLiteSpeed uses the official LiteSpeed repository bootstrap and include files; TLS attachment still requires its provider-specific follow-up.
- A replica is not a backup. Replication, backups, and restore drills solve different problems.
- Plain FTP is rejected unless `allow_plaintext` is explicitly enabled.

The design and remaining production-hardening work are in [docs/architecture.md](docs/architecture.md).
