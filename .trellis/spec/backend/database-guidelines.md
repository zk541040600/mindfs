# Database Guidelines

> Database patterns and conventions for this project.

---

## Overview

<!--
Document your project's database conventions here.

Questions to answer:
- What ORM/query library do you use?
- How are migrations managed?
- What are the naming conventions for tables/columns?
- How do you handle transactions?
-->

(To be filled by the team)

---

## Query Patterns

<!-- How should queries be written? Batch operations? -->

(To be filled by the team)

---

## Migrations

<!-- How to create and run migrations -->

(To be filled by the team)

---

## Naming Conventions

<!-- Table names, column names, index names -->

(To be filled by the team)

---

## Session History and Metadata Projection

MindFS stores each session's durable exchange history in append-only
`.mindfs/sessions/<session-key>.jsonl`. The SQLite `sessions` table is the
metadata projection used for list ordering, pagination, and incremental
`updated_at` filters; it is not the exchange authority.

An exchange append and the SQLite metadata update cross two storage systems and
cannot be one atomic transaction. A process may stop after a valid JSONL append
but before `sessions.updated_at` advances. Every newly opened Session Manager
must therefore reconcile the projection before applying list/count time filters:

- advance `updated_at` only when a valid exchange timestamp is newer;
- never rewrite JSONL or move metadata time backward;
- do not derive `agent_ctx_seq` from log length—a user-only interrupted turn was
  persisted but may not have settled in the Agent runtime;
- keep persisted request IDs optional so pre-migration JSONL remains readable;
- log repair counts and errors without logging prompts or exchange content.

A regression for this boundary must simulate JSONL append success with stale
SQLite metadata, restart the manager, query with `ListOptions.AfterTime`, and
verify both result visibility and durable metadata repair.

## Common Mistakes

- Treating a successful JSONL append as proof that list metadata was also
  updated. A restart between those writes makes preserved history invisible to
  incremental session lists until the projection is reconciled.
- Automatically replaying a user-only interrupted turn. Preserve and surface
  the input, but do not risk duplicate tool side effects.
