# ADR-0002: Metadata-only persistence

- Status: accepted
- Date: 2026-09-04

## Context

A local message mirror or search index would create a second durable copy of
Telegram content. It would need deletion synchronization, retention policy,
protected-content enforcement, encryption and backup rules, and a separate
contractual decision for AI indexing. The MVP can satisfy its bounded read and
search workflows against Telegram without that copy.

## Decision

SQLite stores metadata required for correctness and authorization only:

- schema migration history and authorization epoch;
- kinded peer IDs and session-bound access hashes;
- update checkpoints and degradation markers;
- policy, consent, named-scope, and policy-revision metadata;
- content-free audit outcomes and retention state.

It must not store message bodies, snippets, search queries, quoted or replied
third-party text, attachments, embeddings, access credentials, session bytes,
or durable media. Access hashes remain private to the Telegram adapter even
though they are correctness metadata.

Keychain stores the Telegram session, `api_hash`, and integrity keys. Media may
only exist as a bounded transient stream or file governed by a short-lived,
reauthorized handle; a durable cache is excluded.

## Consequences

- Remote reads may remain available during some metadata-reconciliation
  degradation, but results must distinguish live fetches from stale metadata.
- Search is bounded per authorized peer or named scope; account-wide search
  followed by filtering is forbidden.
- Backup and audit tooling can operate without becoming a Telegram-content
  export path.
- Adding any content persistence, FTS, or embeddings requires a new ADR,
  threat model, retention/deletion design, and current terms decision.

