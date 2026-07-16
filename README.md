# poormanwebctrl

`poorman` is a dependency-light Go CLI and guided terminal UI for provisioning and operating a self-hosted web server. Every change is generated as a readable plan before it runs.

## Included workflows

- Nginx, Apache, or OpenLiteSpeed package/service setup
- Static and PHP virtual hosts
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

## Build and start

```sh
go build -o bin/poorman ./cmd/poorman
./bin/poorman tui
./bin/poorman plan
```

Provisioning is Linux-only. Ubuntu/Debian, RHEL-family Linux, and Alpine are modeled. Development and tests work on macOS.

## Main commands

```text
poorman tui                         guided configuration
poorman init                        write a complete starter configuration
poorman plan [-f FILE]              preview every action
poorman apply [-f FILE] [--yes]     apply locally
poorman status [-f FILE]            run health checks
poorman backup [--yes]              run the installed backup job now
poorman replica status [-f FILE]    show replication health/lag data
poorman replica promote [-f FILE]   promote a configured replica
```

## Secrets

Configuration stores environment-variable names, never passwords. Export the values before `apply`:

```sh
export POORMAN_DB_PASSWORD='use-a-password-manager-generated-value'
export POORMAN_WP_ADMIN_PASSWORD='another-unique-value'
./bin/poorman apply
```

Secret-backed command input is redacted from plans. SQL secrets are escaped before being sent over standard input rather than included in process arguments.

## Replication

Create one configuration per database node. Set `database.role` to `primary` or `replica`, give every MariaDB node a unique `node_id`, and restrict the primary with `allowed_cidr`. See [PostgreSQL primary](examples/postgresql-primary.json) and [PostgreSQL replica](examples/postgresql-replica.json).

Promotion is deliberately guarded. Before running it, fence the failed primary to prevent split brain. `poorman replica promote` requires typing `PROMOTE` unless `--yes` is supplied.

PostgreSQL still requires the precise `pg_hba.conf` rule printed in the plan. The tool does not guess trusted networks or edit a version-dependent authentication file blindly.

## Operational cautions

- Review `plan` on the actual target OS before applying.
- Test backups by restoring them to a separate host.
- OpenLiteSpeed uses the official LiteSpeed repository bootstrap and include files; TLS attachment still requires its provider-specific follow-up.
- A replica is not a backup. Replication, backups, and restore drills solve different problems.
- Plain FTP is rejected unless `allow_plaintext` is explicitly enabled.

The design and remaining production-hardening work are in [docs/architecture.md](docs/architecture.md).
