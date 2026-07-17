# Architecture

## Shape of the system

```text
CLI / guided TUI
       |
configuration validation
       |
desired-state provider graph
       |
auditable structured plan
       |
local privileged executor
```

The executor supports structured commands, user switching, managed directories, managed files, and idempotent line insertion. It does not execute provider-generated shell fragments. Commands can carry redacted standard input populated from environment variables.

## Provider order

Plans follow dependency order:

1. Install packages.
2. Create users and access policy.
3. Start and configure the database.
4. Create document roots and virtual hosts.
5. Install WordPress when selected.
6. Validate and restart services.
7. Apply firewalls and certificates.
8. Install backup jobs, enforce local retention, and optionally copy/prune timestamped runs in S3.

Database management is a declarative subgraph inside step 3. A database
instance can contain multiple logical databases, users, tables/columns, and
explicit permissions. Identifiers are validated before planning and quoted in
provider-specific SQL. On a physical replica, this subgraph is deliberately
not emitted: the primary owns schema and ACL changes, while replication carries
them to the read-only instance.

Managed files are intentionally owned by poorman and replaced on apply. Every managed file and directory declares an explicit owner and group; web roots use the configured deployment owner with the active web server's runtime group. The managed inventory records vhost configuration paths so removed sites and provider switches delete obsolete poorman-owned files before the desired files are regenerated. Unrelated system configuration is left alone.

Each apply also maintains `/var/lib/poorman/managed.json`. It records the
configuration file and system service identity for every poorman-managed
service. The inventory is merged per configuration, so multiple configuration
files can safely manage separate database instances on one host. Before an
apply provisions a changed instance, the executor stops the previous managed
service; data directories are deliberately retained for recovery or manual
migration.

## Replication safety model

PostgreSQL streaming replication and MariaDB GTID replication share inventory concepts but have provider-specific actions. Promotion is a manual, guarded operation rather than automatic failover. Correct promotion requires external fencing, client redirection, verification, and a config update.

PostgreSQL `pg_hba.conf` placement is version/package dependent and authentication trust is security-sensitive. The planner emits the exact CIDR-scoped rule but leaves insertion to the operator. MariaDB receives a managed replication fragment with unique node ID, row binlogs, GTID strict mode, and read-only replicas.

Same-host MariaDB replicas on systemd distributions are separate service instances. The replica has an isolated data directory, configuration, runtime socket/PID directory, log, TCP port, seed snapshot, and backup job. Its unit is ordered after the primary during boot but deliberately has no hard service dependency, allowing either database process to fail without systemd stopping the other. This protects against a database-process failure, not a shared host, disk, kernel, or power failure.

Promotion writes the independent instance's read-only setting to `OFF` and targets its private socket. After the inventory role changes to `primary`, subsequent plans continue managing the same data directory, port, socket, service, and instance-specific backup rather than falling back to the distribution's default MariaDB service.

## What “feature complete v1” means

The v1 surface covers local installation, configuration, inspection, backup, and replica operations for the advertised components. Production-hardening still includes:

- integration tests inside supported distro containers/VMs;
- transaction journal and rollback of managed files;
- remote execution over SSH and multi-host orchestration;
- encrypted inventory/secrets backend integration;
- backup restore commands and automated restore verification;
- OpenLiteSpeed admin API integration for certificate attachment;
- version-aware PHP-FPM pool discovery;
- rolling upgrades and automatic failover controllers;
- a full-screen dashboard beyond the guided TUI.

Those are intentionally not claimed as implemented. The TUI includes long-term read-only operations plus a guardrails and backups flow for enabling protections, running backups, and inspecting backup artifacts. It does not yet persist time-series metrics, provide remote/multi-host management, or verify restores automatically.
