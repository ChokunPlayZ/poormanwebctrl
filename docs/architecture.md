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
8. Install backup jobs.

Managed files are intentionally owned by poorman and replaced on apply. Unrelated system configuration is left alone.

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

Those are intentionally not claimed as implemented. The guided TUI now includes a read-only long-term operations area for host stats, recent service logs, and backup inventory; it does not yet persist time-series metrics or provide remote/multi-host management.
