# Filesystem Boundary Contracts

## Scenario: Managed root and metadata path containment

### 1. Scope / Trigger

- Trigger: code resolves a relative path below `fs.RootInfo.RootPath` for directory listing, file reading, raw download, file metadata, upload destination creation, watcher registration, or `.mindfs` state access.
- A lexical `filepath.Join`/`filepath.Rel` check is insufficient because an existing symbolic link can redirect a path outside the managed root.
- The `.mindfs` metadata namespace is a stricter sub-boundary: callers must not escape it with `..` or an absolute path.

### 2. Signatures

- `func (r RootInfo) ValidateRelativePath(relPath string) error`
- `func (r RootInfo) ReadFile(pathRel string, maxBytes int64, cursor int64, readMode string) (ReadResult, error)`
- `func (r RootInfo) OpenFile(pathRel string) (*os.File, os.FileInfo, string, error)`
- `func (r RootInfo) StatFile(pathRel string) (os.FileInfo, string, error)`
- `func (r RootInfo) EnsureMetaDir() (string, error)`
- `func (r RootInfo) WriteMetaFile(path string, data []byte) error`
- Internal boundary helpers: `resolvePathWithinRoot`, `validateResolvedPath`, and `resolveMetaPath` in `server/internal/fs/fs.go`.

### 3. Contracts

- First validate that a relative path is lexically inside the root. Then resolve each existing path component with `Lstat`/`EvalSymlinks` and require the resolved target to remain inside the resolved root.
- A missing suffix is allowed for a future write only after every existing parent component has passed the same check. A dangling symbolic link is rejected rather than treated as a missing directory.
- A symbolic link whose final target remains inside the root is supported. `ListEntries` may follow it for directory metadata; a link targeting outside the root remains visible as a link but is not followed.
- `ReadFile`, `OpenFile`, `StatFile`, `ListEntries`, metadata access, uploads, and watcher registration must resolve through the same checked relative-path boundary before operating on the filesystem.
- `.mindfs` methods accept only a relative path lexically contained below `.`. `../state.json` and absolute metadata paths must fail before any directory creation or file write.
- `NormalizePath` is string normalization and may be used before a root exists. It must preserve its no-I/O behavior; actual filesystem operations perform the resolved-target validation at their boundary.

### 4. Validation & Error Matrix

| Condition | Result |
| --- | --- |
| Absolute relative-path input | `absolute path not allowed` |
| Lexical `..` traversal outside root | `path outside root` |
| Existing link or link component targets outside root | `path outside root` |
| Existing link target stays inside root | operation proceeds |
| Dangling link in an operation path | resolve failure; no write is attempted |
| Absolute metadata path | `absolute meta path not allowed` |
| Metadata `..` escape | `meta path outside metadata directory` |
| `.mindfs` itself links outside root | `path outside root`; no metadata file is created outside |

### 5. Good / Base / Bad Cases

- Good: `root/linked -> root/target` can be listed and read as `linked/file.txt`.
- Base: a missing normal upload subdirectory is created below the managed root after its existing parents have been checked.
- Bad: `root/outside -> /private/data` is accepted because `filepath.Join(root, "outside/secret")` is lexically under `root`; `os.Open` then follows the link and exposes the external file.
- Bad: `filepath.Join(".mindfs", filepath.Clean("../state.json"))` collapses to `state.json`, allowing metadata code to operate outside `.mindfs`.

### 6. Tests Required

- `server/internal/fs/fs_test.go`: an in-root directory link remains readable; an outside link is rejected by list/read/open/stat; metadata rejects `..` and an outside `.mindfs` link.
- `server/internal/api/usecase/usecase_test.go`: an upload directory link outside the root returns `path outside root` and does not create the uploaded file at the target.
- Run `/root/.local/go1.25/bin/go test ./server/internal/fs ./server/internal/api/usecase -count=1`; include these packages in full-suite regression after changes.

### 7. Wrong vs Correct

#### Wrong

```go
clean := filepath.Clean(filepath.Join(root, relPath))
rel, _ := filepath.Rel(root, clean)
if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
    return errors.New("path outside root")
}
return os.Open(clean)
```

The lexical result may be inside `root` while `clean` contains a symbolic link to an external target.

#### Correct

```go
clean, err := resolvePathWithinRoot(root, relPath)
if err != nil {
    return err
}
if err := r.validateResolvedPath(clean); err != nil {
    return err
}
return os.Open(clean)
```

The second check walks existing components and rejects an external symbolic-link target before the operation follows it.
