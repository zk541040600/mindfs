# Backend Development Guidelines

> Best practices for backend development in this project.

---

## Overview

This directory contains guidelines for backend development. Fill in each file with your project's specific conventions.

---

## Guidelines Index

| Guide | Description | Status |
|-------|-------------|--------|
| [Directory Structure](./directory-structure.md) | Module organization and file layout | To fill |
| [Database Guidelines](./database-guidelines.md) | ORM patterns, queries, migrations | To fill |
| [Error Handling](./error-handling.md) | Error types, handling strategies | To fill |
| [E2EE Security](./e2ee-security.md) | Replay, handshake, and pairing-secret contracts | Active |
| [Filesystem Boundaries](./filesystem-boundaries.md) | Managed-root, symbolic-link, and metadata containment | Active |
| [Command Execution](./command-execution.md) | Persistent shell identity, configured-shell selection, and cleanup | Active |
| [Hosted Agent Configuration](./hosted-agent-config.md) | Remote metadata refresh without remote process authority | Active |
| [Self Update](./self-update.md) | Verified package installation and Windows detached restart layout | Active |
| [Quality Guidelines](./quality-guidelines.md) | Code standards, forbidden patterns | To fill |
| [Logging Guidelines](./logging-guidelines.md) | Structured logging, log levels | To fill |

---

## How to Fill These Guidelines

For each guideline file:

1. Document your project's **actual conventions** (not ideals)
2. Include **code examples** from your codebase
3. List **forbidden patterns** and why
4. Add **common mistakes** your team has made

The goal is to help AI assistants and new team members understand how YOUR project works.

---

**Language**: All documentation should be written in **English**.
