# ADR-0002: MVP Evidence and Tenant Security Boundaries

Status: Accepted
Date: 2026-09-12

## Context

The MVP stores raw HTML, snapshots, screenshots, and notification configuration
outside the request process. Tenant IDs are present in the domain model, but
relational foreign keys alone did not prevent a resource from referencing an
object belonging to another tenant. Evidence metadata also was not rechecked
when content was downloaded or exported.

## Decision

- Evidence download and report export read the object through a bounded reader
  and verify both the recorded size and SHA-256 before returning any bytes.
- PostgreSQL adds composite tenant-aware foreign keys for owners, assets,
  policies, baselines, jobs, evidence, incidents, deliveries, and audit actors.
- Webhook signatures cover an issued UTC timestamp and the request body. Webhook
  consumers must reject stale timestamps and deduplicate deliveries.
- The original evidence object remains append-only. The migration only adds
  constraints and indexes; it does not rewrite or delete evidence.

## Consequences

Evidence exports above the MVP verification limit are rejected until the
bounded streaming path is extended. Existing databases must apply migration
`000003_tenant_integrity` after validating any pre-existing cross-tenant rows.
The webhook receiver contract now includes `X-WebMonitoring-Timestamp`.

## Rollback

Apply `000003_tenant_integrity.down.sql` only after incident review. Removing
the constraints weakens tenant isolation and must be accompanied by an
explicit approval and audit record.
