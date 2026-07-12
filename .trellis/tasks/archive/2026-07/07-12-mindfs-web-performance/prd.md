# Optimize MindFS web cold-start and interaction performance

## Goal

Reduce MindFS cold-start transfer and JavaScript startup work, and bound high-frequency streaming UI updates, without weakening offline/runtime caching, relay paths, native app-shell builds, or session reliability.

## Requirements

1. Normal production web builds must generate Brotli and gzip siblings for compressible hashed JavaScript and CSS assets when compression produces a smaller representation.
2. Android/Harmony app-shell builds must not include web-server-only `.br` or `.gz` siblings.
3. The Go static handler must negotiate precompressed assets from `Accept-Encoding`, respect disabled encodings and quality values, prefer the best supported representation, preserve the original content type, emit `Vary: Accept-Encoding`, and fall back to the identity file.
4. Compression must work for both direct `/assets/` paths and relayed `/mindfs-assets/` aliases without changing existing cache policy or frontend rewriting behavior.
5. Optional heavy frontend features must be dynamically imported only when rendered; the core session shell must remain directly available.
6. Dynamic chunks must remain compatible with the existing service-worker runtime cache. The optimization must not eagerly precache every Mermaid implementation.
7. High-frequency stream notifications must cause at most one non-terminal React version update per animation frame; recovery, completion, and error state must remain promptly observable.
8. A repeatable build check must report the entry asset sizes, require usable precompressed siblings, and enforce a meaningful entry budget instead of hiding Vite warnings.

## Constraints

- Preserve request-ID, pending-state, reconnect, cancellation, and shutdown semantics from the archived reliability task.
- Do not remove Markdown raw HTML, math, syntax highlighting, or other user-visible features in this task.
- Do not introduce a new frontend framework or generic compression dependency.
- Preserve immutable caching for hashed assets and no-cache behavior for `index.html` and `service-worker.js`.
- Keep changes backward compatible when older `web/dist` directories do not contain compressed siblings.
- Production restart/deployment requires an explicit approval gate after all local validation passes.

## Acceptance Criteria

- [x] A clean normal web build emits smaller `.br` and `.gz` siblings for the entry JS/CSS, and the build-performance check passes.
- [x] App-shell build output contains no `.br` or `.gz` files.
- [x] Focused Go tests cover Brotli, gzip, q-value/disabled fallback, content type, `Vary`, cache headers, relay alias, and identity fallback.
- [x] Optional JSON renderer, file/Git viewers, and task dialogs are absent from the entry chunk and load through explicit suspense boundaries; the core ActionBar/TokenEditor remains synchronous.
- [x] Entry JS minified size is at least 15% below the 2,128.58 kB baseline, with an encoded entry budget recorded by the build check.
- [x] Stream chunk bursts are animation-frame coalesced and terminal/error paths cancel or flush pending work safely.
- [x] `go test ./...`, `go vet ./...`, web typecheck/build, app-shell builds, complete Playwright, bridge syntax, and whitespace checks pass.
- [x] Bundle comparison and residual trade-offs are recorded in task evidence.
- [x] If deployment is approved, direct and relay/static behavior, response encoding, browser loading, service health, and rollback readiness are verified against the running service.
- [x] Approved source/evidence commits are created, task metadata validates, and the task is archived; the completed session is recorded afterward as repository bookkeeping.
