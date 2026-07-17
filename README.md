# poormanwebctrl

`poorman` is a dependency-light Go CLI and guided terminal UI for provisioning and operating a self-hosted web server. Every change is generated as a readable plan before it runs.

## Included workflows

- Nginx, Apache, or OpenLiteSpeed package/service setup
- Static and PHP virtual hosts
- Multiple virtual hosts with aliases, plus guided add/edit/remove management
- PostgreSQL or MariaDB database chains: databases, users, tables, and explicit ACL/grant permissions
- PostgreSQL streaming-replica and MariaDB GTID primary/replica plans
- Replication status and guarded replica promotion
- Linux users, SSH keys, and SFTP-only groups
- Explicit opt-in vsftpd support (SFTP is the safe default)
- WordPress download, configuration, and installation with wp-cli
- Per-domain Let's Encrypt certificates for Nginx and Apache
- UFW/firewalld rules, including CIDR-scoped database replication
- Scheduled site/database backups and on-demand backups, with configurable local retention
- Optional offsite S3 copies with separate retention, AWS profiles/regions, and S3-compatible endpoints
- Local service, configuration, and virtual-host health checks
- TUI firewall management: status, policy preview/apply, and guarded disable
- TUI long-term operations: host resource stats, service logs, and backup inventory
- TUI guardrails and recovery: enable HTTPS, firewall, and backups; edit backup settings; run backups; and inspect backup artifacts

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

Press `Ctrl-C` during guided setup and Poorman will ask whether you want to cancel before discarding the setup. During an in-progress operation, `Ctrl-C` stops it safely: completed steps are kept, the active command is canceled, and remaining steps are not started; rerun `apply` to continue from the idempotent plan.

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
poorman replica setup [-f FILE]       add a same-host replica to FILE
poorman replica setup -f REPLICA_FILE --from PRIMARY_FILE
                                      configure a replica on another host
```

When a configuration already exists, `poorman tui` opens the operations dashboard. Choose **long-term operations** for read-only host capacity, recent systemd journal logs for configured services, and the files currently present in the configured backup destination.

Each successful apply records poorman-managed services in `/var/lib/poorman/managed.json`. This lets the dashboard show the primary and local replica from one configuration, as well as database instances from remote-host configurations, and lets a later apply retire an old managed replica service when its port, data directory, or provider changes. Existing data directories are retained.

After the first apply, poorman is authoritative for every configuration file it records in that inventory. Every apply rewrites all current managed virtual hosts, removes obsolete managed vhost files after a site removal or web-server switch, and restores explicit service-appropriate ownership. Manual edits to poorman-managed configuration files are intentionally overwritten. The server login MOTD states this policy.

Choose **Virtual hosts** in that dashboard to list, add, edit, or remove domains. Every host is planned independently, including its document root, aliases, runtime, HTTPS choice, WordPress setup, and managed server configuration. The edit screen lists every editable setting and its current value; move to one and press Enter to change only that setting.

A virtual host owner may be declared under `access.users` or may be an existing system account. Poorman creates and configures declared access users; for an undeclared owner, it verifies that the account exists during apply and otherwise leaves the account untouched.

Choose **Stack settings** to adjust the web server, database and replication role, certificate email, firewall, and backup destination/schedule after setup. HTTPS is enabled or disabled independently in each virtual host's settings.

Choose **Database management** in the dashboard to add logical databases, database users, tables, and explicit permissions. You can also remove database, user, table, and ACL definitions. Removal is deliberately non-destructive: it updates the desired configuration and clears dependent definitions, but does not drop live databases, tables, accounts, or existing grants. The plan validates and quotes all managed identifiers. The older `database.name`, `database.user`, and `database.password_env` fields remain supported as a single-database shorthand; use `database.databases`, `database.users`, and `database.permissions` for a full chain. See [the database-chain example](examples/database-chain.json).

Choose **guardrails & backups** for the fast operational path: review per-domain HTTPS coverage, turn the firewall or scheduled backups on or off, set the shared certificate contact email, configure the backup destination and cron schedule, run a backup immediately, or inspect the backup inventory.

### Backup retention and S3

Local backup runs are timestamped in UTC and deleted after `retention_days` (14 days when omitted). An optional S3 copy can use its own retention period; when the offsite value is omitted it inherits the local policy.

```json
"backups": {
  "enabled": true,
  "destination": "/var/backups/poorman",
  "schedule": "0 3 * * *",
  "retention_days": 14,
  "offsite": {
    "provider": "s3",
    "bucket": "company-server-backups",
    "prefix": "production/web-01",
    "region": "ap-southeast-1",
    "retention_days": 90
  }
}
```

The generated job uploads a completed run before applying either retention policy. If an S3 upload fails, local cleanup does not run, leaving the local copy available for recovery or manual upload. S3 pruning only considers timestamp-shaped run directories inside the configured prefix.

Poorman installs the distribution's AWS CLI package and uses its normal credential chain; it never writes access keys into the configuration or backup script. Prefer an instance role. Because scheduled backups run as root, a named `profile` must be available to root, and environment credentials must be provided to the scheduled job's environment. The S3 identity needs `s3:PutObject`, `s3:ListBucket`, and `s3:DeleteObject` for the configured bucket/prefix. `endpoint` is optional for S3-compatible storage.

During a new guided setup, the database section asks for `standalone`, `primary`, or `replica`. Use `poorman replica setup -f poorman.json` (or **guided replica setup** in the dashboard) to add a same-host replica to an existing stack. The wizard promotes a standalone database to `primary` and writes the replica's port, data directory, and node identity into `database.local_replica` in that same file. For a replica on another host, use `--from`; the remote host keeps its own configuration because it has a separate apply target.

Logical database objects are applied only on standalone and primary instances. Replica plans skip database/table/user/grant writes and configure replication instead, preventing accidental writes to a physical read-only replica. Manage the chain on the primary configuration and let replication carry it to replicas. MariaDB replicas may additionally declare an explicitly local user (`"local": true`); poorman disables binary logging for that account change. PostgreSQL hot standbys cannot create local roles, so their users must come from the primary.

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

Use one configuration per host. A remote database node has its own file and apply target; a same-host replica is declared under the primary database's `local_replica`. Give every MariaDB process a unique `node_id`, and restrict the primary with `allowed_cidr`. See [PostgreSQL primary](examples/postgresql-primary.json) and [PostgreSQL replica](examples/postgresql-replica.json) for separate-host configurations.

For a primary and replica on the same machine, run `poorman replica setup -f poorman.json`. PostgreSQL uses a separate port and dedicated data directory. MariaDB on Debian/Ubuntu and RHEL-family systems gets its own data directory, config, socket, PID, log, port, systemd service, and seed snapshot; see [the same-host MariaDB replica example](examples/mariadb-replica-same-host.json). One `poorman apply -f poorman.json` configures the primary first and then creates and attaches the local replica.

The two database processes are deliberately not coupled with a systemd `Requires=` or `PartOf=` relationship, so one database service crashing does not stop the other. Same-host replication is process-level redundancy only: the primary and replica still share the machine, storage hardware, kernel, and power source. Use a separate host for host-level availability. Same-host MariaDB service generation currently requires systemd; Alpine/OpenRC needs a separate replica host.

Promotion is deliberately guarded. Before running it, fence the failed primary to prevent split brain. `poorman replica promote` requires typing `PROMOTE` unless `--yes` is supplied.

Promoting a same-host replica makes its independent instance writable, but client redirection and the post-failover configuration change remain explicit. Fence the old primary, point applications at the promoted port, and replace the failed topology before the next normal apply.

PostgreSQL still requires the precise `pg_hba.conf` rule printed in the plan. The tool does not guess trusted networks or edit a version-dependent authentication file blindly.

## Operational cautions

- Review `plan` on the actual target OS before applying.
- Test backups by restoring them to a separate host.
- OpenLiteSpeed uses the official LiteSpeed repository bootstrap and include files; TLS attachment still requires its provider-specific follow-up.
- A replica is not a backup. Replication, backups, and restore drills solve different problems.
- Plain FTP is rejected unless `allow_plaintext` is explicitly enabled.

The design and remaining production-hardening work are in [docs/architecture.md](docs/architecture.md).

## Extending poorman

Commands and provisioning features are registered instead of hard-coded in a
central router. A feature keeps its platform checks, package requirements, and
plan contribution together; optional feature-specific JSON belongs under the
top-level `extensions` object. See the
[extension points](docs/architecture.md#extension-points) for the supported
workflow and code ownership map.
