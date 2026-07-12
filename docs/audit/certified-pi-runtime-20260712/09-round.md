# Certified round 09 — model and thinking-mode state refresh visibility

- Started: `2026-07-12T10:10:05,128172048+08:00`
- Corrected audit completed: `2026-07-12T10:10:31,910962603+08:00`
- Previous record SHA-256: `6753c6f2ae76bd4ffb0eabdeb408e836f5eb5bdd51f254fb8e785a328a199552`
- Authoritative validation SHA-256: `eae0d10d486f4dd32414bc178097a1f6d75a47a29a000dd6854b79001697c4bb` (`09-validation-v2.log`)
- Superseded log: `09-validation.log` used an incomplete test-name filter and was rejected before this record.

## Audit

Inspected model/mode setters, list paths, and state refresh at `server/internal/agent/pi_sdk_runtime/session.go:1503–1600,1700–1736`. `ListModes` requires live state and propagates failure. `ListModels` first performs a live model-list request; its optional current model may remain empty if the secondary refresh fails, while the usable model list remains valid.

## Finding and action

No new defect was confirmed. No source change was made. Propagating the secondary `ListModels` refresh error would discard a successfully retrieved model list and change its existing partial-data contract.

## Verification

Normal model/mode controls, closed-mode reporting, and model normalization/deduplication all passed in `09-validation-v2.log`.

## Residual risk

Callers can receive a valid model list with no current selection when the secondary state refresh fails. They must treat CurrentModelID as optional.

Status: **DONE**. This record was written before certified round 10 started.
