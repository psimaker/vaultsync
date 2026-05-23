# Conflict Skip-Family Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make "Always skip on this iPhone" actually skip a file *and* its future and existing sync-conflict copies, closing the bug in issue #8.

**Architecture:** Add a Go-bridge function to delete a file's conflict copies; have Swift wrap the "skip" gesture into a single semantic operation that writes a pair of `.stignore` patterns (`X` + `X.sync-conflict-*`), removes existing conflict copies on disk, rescans, and refreshes the UI. The Sync Filters list groups paired patterns as a single row.

**Tech Stack:** Go 1.26 + gomobile (bridge), Swift 6 / SwiftUI (iOS app), XcodeGen (project.yml), Make (xcframework build).

**Spec:** `docs/superpowers/specs/2026-05-23-conflict-skip-family-design.md`

**Branch:** Work is on `fix/issue-8-conflict-skip-family` (already created from `main`). All commits land there; the user merges to `main` at the end.

**Commit policy:** Commits use Conventional Commits style (`feat:`, `fix:`, `chore:`, `docs:`). **No `Co-Authored-By:` trailer** on any commit. **No `git push`** at any point.

---

## File Map

### Go bridge
- **Modify** `go/bridge/conflicts.go` — add `RemoveConflictFilesForOriginal(folderID, originalPath string) string`
- **Modify** `go/bridge/conflicts_test.go` — add tests for the new function
- **Rebuild** `go/build/SyncBridge.xcframework/` — generated artifact, gitignored

### Swift services
- **Modify** `ios/VaultSync/Services/SyncBridgeService.swift` — add `removeConflictFilesForOriginal(folderID:originalPath:)` wrapper
- **Modify** `ios/VaultSync/Services/SyncthingManager.swift` — add `conflictGlob(forOriginalPath:)` helper and `skipFileAndCleanupConflicts(folderID:originalPath:)` method

### Swift views
- **Modify** `ios/VaultSync/Views/ConflictDiffView.swift` — `skipThisFile()` calls the new method; alert copy updated
- **Modify** `ios/VaultSync/Views/IgnorePatternsView.swift` — Custom Patterns section renders paired patterns as one row; swipe-delete removes both

### Swift tests
- **Create** `ios/VaultSyncTests/SkipFamilyTests.swift` — unit tests for `conflictGlob` and pair detection

### Localization
- **Modify** `ios/VaultSync/en.lproj/Localizable.strings`
- **Modify** `ios/VaultSync/de.lproj/Localizable.strings`
- **Modify** `ios/VaultSync/zh-Hans.lproj/Localizable.strings`

### Versioning + docs
- **Modify** `ios/project.yml` — bump to `1.3.2` / build `24`
- **Modify** `CHANGELOG.md` — add `[1.3.2]` section
- **Modify** `docs/sync-filters-ux.md` — update §6 ("Conflict → Ignore")

---

## Task 1: Failing Go test for `RemoveConflictFilesForOriginal`

**Files:**
- Modify: `go/bridge/conflicts_test.go` (append at end)

- [ ] **Step 1: Append the failing test**

Append to `go/bridge/conflicts_test.go`:

```go
func TestRemoveConflictFilesForOriginal(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "skipfamily")
	if errMsg := AddFolder("skipfamily", "Skip Family", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Root-level original + two conflict copies (different timestamps/devices).
	os.WriteFile(filepath.Join(folderPath, "notes.md"), []byte("original"), 0o644)
	os.WriteFile(filepath.Join(folderPath, "notes.sync-conflict-20260520-120000-AAA1111.md"), []byte("c1"), 0o644)
	os.WriteFile(filepath.Join(folderPath, "notes.sync-conflict-20260521-130000-BBB2222.md"), []byte("c2"), 0o644)

	// Unrelated file that must not be touched.
	os.WriteFile(filepath.Join(folderPath, "other.md"), []byte("other"), 0o644)
	os.WriteFile(filepath.Join(folderPath, "other.sync-conflict-20260520-120000-CCC3333.md"), []byte("o1"), 0o644)

	// Nested original + nested conflict.
	subDir := filepath.Join(folderPath, "Personal")
	os.MkdirAll(subDir, 0o755)
	os.WriteFile(filepath.Join(subDir, "diary.md"), []byte("d"), 0o644)
	os.WriteFile(filepath.Join(subDir, "diary.sync-conflict-20260520-120000-DDD4444.md"), []byte("d1"), 0o644)

	// Remove conflict copies for "notes.md" only.
	got := RemoveConflictFilesForOriginal("skipfamily", "notes.md")
	var result struct {
		Removed int    `json:"removed"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, got)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Removed != 2 {
		t.Errorf("removed = %d, want 2", result.Removed)
	}

	// Original "notes.md" must survive.
	if _, err := os.Stat(filepath.Join(folderPath, "notes.md")); err != nil {
		t.Errorf("notes.md should still exist: %v", err)
	}

	// Both notes conflict copies must be gone.
	for _, name := range []string{
		"notes.sync-conflict-20260520-120000-AAA1111.md",
		"notes.sync-conflict-20260521-130000-BBB2222.md",
	} {
		if _, err := os.Stat(filepath.Join(folderPath, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been deleted", name)
		}
	}

	// Unrelated "other.*" files must survive.
	if _, err := os.Stat(filepath.Join(folderPath, "other.md")); err != nil {
		t.Errorf("other.md should still exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folderPath, "other.sync-conflict-20260520-120000-CCC3333.md")); err != nil {
		t.Errorf("other.sync-conflict-* should still exist: %v", err)
	}

	// Nested originals and their conflicts in another directory must survive
	// when we ask for the root file only.
	if _, err := os.Stat(filepath.Join(subDir, "diary.sync-conflict-20260520-120000-DDD4444.md")); err != nil {
		t.Errorf("nested conflict should still exist: %v", err)
	}

	// Now ask for nested "Personal/diary.md" and verify only the nested copy goes.
	got = RemoveConflictFilesForOriginal("skipfamily", filepath.Join("Personal", "diary.md"))
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal nested: %v (raw: %s)", err, got)
	}
	if result.Removed != 1 || result.Error != "" {
		t.Errorf("nested call result = %+v, want removed=1 error=\"\"", result)
	}

	// Idempotency: running again returns removed=0, no error.
	got = RemoveConflictFilesForOriginal("skipfamily", "notes.md")
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal idempotent: %v (raw: %s)", err, got)
	}
	if result.Removed != 0 || result.Error != "" {
		t.Errorf("idempotent call = %+v, want removed=0 error=\"\"", result)
	}
}

func TestRemoveConflictFilesForOriginalErrors(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "skipfamilyerr")
	if errMsg := AddFolder("skipfamilyerr", "Skip Family Err", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Unknown folder.
	got := RemoveConflictFilesForOriginal("nonexistent", "x.md")
	if !strings.Contains(got, `"error":"folder not found"`) {
		t.Errorf("unknown folder result = %q, want error 'folder not found'", got)
	}

	// Path traversal.
	got = RemoveConflictFilesForOriginal("skipfamilyerr", "../../etc/passwd")
	if !strings.Contains(got, `"error":"invalid path: outside folder root"`) {
		t.Errorf("traversal result = %q, want invalid-path error", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/go
go test -tags noassets -run TestRemoveConflictFilesForOriginal ./bridge/
```

Expected: FAIL with `undefined: RemoveConflictFilesForOriginal`.

---

## Task 2: Implement `RemoveConflictFilesForOriginal`

**Files:**
- Modify: `go/bridge/conflicts.go` (append at end)

- [ ] **Step 1: Append the implementation**

Append to `go/bridge/conflicts.go`:

```go
// RemoveConflictFilesForOriginal removes every sync-conflict copy of the file
// at originalPath inside the given folder. The original file is NOT touched.
//
// Returns a JSON string of the form:
//
//	{"removed": <int>, "error": "<msg or empty>"}
//
// Symmetric with GetConflictFilesJSON's JSON-return style — keeps the gomobile
// surface uniform (no tuple returns across the bridge).
func RemoveConflictFilesForOriginal(folderID, originalPath string) string {
	type result struct {
		Removed int    `json:"removed"`
		Error   string `json:"error"`
	}
	emit := func(r result) string {
		data, err := json.Marshal(r)
		if err != nil {
			return `{"removed":0,"error":"marshal failed"}`
		}
		return string(data)
	}

	folders := getFolderConfigs()
	if folders == nil {
		return emit(result{Error: "syncthing not running"})
	}

	folder, exists := folders[folderID]
	if !exists {
		return emit(result{Error: "folder not found"})
	}

	// Validate the original path is inside the folder root.
	absOriginal, err := safePath(folder.Path, originalPath)
	if err != nil {
		return emit(result{Error: "invalid path: outside folder root"})
	}

	dir := filepath.Dir(absOriginal)
	baseName := filepath.Base(originalPath)
	ext := filepath.Ext(baseName)
	stem := strings.TrimSuffix(baseName, ext)
	// Prefix every conflict copy of this file starts with.
	conflictPrefix := stem + ".sync-conflict-"

	entries, err := os.ReadDir(dir)
	if err != nil {
		// If the directory does not exist there are simply no conflicts to remove.
		if os.IsNotExist(err) {
			return emit(result{Removed: 0})
		}
		return emit(result{Error: fmt.Sprintf("read dir: %v", err)})
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, conflictPrefix) {
			continue
		}
		// Must also match the canonical conflict regex so we only delete real
		// Syncthing-generated copies, not user files that happen to share the prefix.
		matches := conflictPattern.FindStringSubmatch(name)
		if matches == nil {
			continue
		}
		// Defensive: matched stem must equal what we expected.
		if matches[1] != stem {
			continue
		}
		// Extension on the conflict copy must equal the original's extension
		// (handles files where stem itself contains dots).
		if matches[4] != ext {
			continue
		}
		fullPath := filepath.Join(dir, name)
		if err := os.Remove(fullPath); err != nil {
			return emit(result{Removed: removed, Error: fmt.Sprintf("remove %s: %v", name, err)})
		}
		removed++
	}

	return emit(result{Removed: removed})
}
```

- [ ] **Step 2: Run tests to verify they pass**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/go
go test -tags noassets -run TestRemoveConflictFilesForOriginal ./bridge/
```

Expected: `PASS` for both `TestRemoveConflictFilesForOriginal` and `TestRemoveConflictFilesForOriginalErrors`.

- [ ] **Step 3: Run the full bridge test suite to confirm no regression**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/go
go test -tags noassets ./bridge/
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add go/bridge/conflicts.go go/bridge/conflicts_test.go
git commit -m "feat(bridge): add RemoveConflictFilesForOriginal for skip cleanup

Removes every sync-conflict copy of a given file in a folder, leaving
the original on disk. Used by the 'Always skip on this iPhone' flow to
actively clear conflict-copy leftovers when the user opts out of a file."
```

---

## Task 3: Rebuild the xcframework

**Files:** No tracked files change here — `go/build/` is gitignored. This step is required so the Swift side sees the new export.

- [ ] **Step 1: Build the xcframework**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/go
make xcframework
```

Expected: `Built build/SyncBridge.xcframework` and a size line.

- [ ] **Step 2: Verify the new symbol is exported**

```bash
grep -q "BridgeRemoveConflictFilesForOriginal" /Users/anon/Nextcloud/Projekte/vaultsync/go/build/SyncBridge.xcframework/ios-arm64/SyncBridge.framework/Headers/Bridge.objc.h && echo "OK"
```

Expected output: `OK`.

(No commit — the rebuilt framework is gitignored.)

---

## Task 4: Swift wrapper in `SyncBridgeService`

**Files:**
- Modify: `ios/VaultSync/Services/SyncBridgeService.swift` (after `keepBothConflict`, near line 215)

- [ ] **Step 1: Add the Swift wrapper**

Insert this method into `SyncBridgeService` immediately after `keepBothConflict` (before the `// MARK: - Pending folder shares` section):

```swift
    /// Remove every sync-conflict copy of the file at originalPath inside the folder.
    /// Returns `(removed, nil)` on success or `(0, errorMessage)` on failure.
    static func removeConflictFilesForOriginal(folderID: String, originalPath: String) -> (removed: Int, error: String?) {
        let raw = BridgeRemoveConflictFilesForOriginal(folderID, originalPath)
        struct Payload: Decodable {
            let removed: Int
            let error: String
        }
        guard let data = raw.data(using: .utf8),
              let decoded = try? JSONDecoder().decode(Payload.self, from: data) else {
            return (0, "unparseable bridge response")
        }
        if !decoded.error.isEmpty {
            return (0, decoded.error)
        }
        return (decoded.removed, nil)
    }
```

- [ ] **Step 2: Confirm the file compiles**

This is verified later as part of the full build (Task 9). No separate compile step here — Swift sources are only compiled via Xcode/xcodebuild.

- [ ] **Step 3: Commit**

```bash
git add ios/VaultSync/Services/SyncBridgeService.swift
git commit -m "feat(ios): wrap RemoveConflictFilesForOriginal in SyncBridgeService"
```

---

## Task 5: Failing Swift tests for `conflictGlob`

**Files:**
- Create: `ios/VaultSyncTests/SkipFamilyTests.swift`

- [ ] **Step 1: Create the failing tests**

Create `ios/VaultSyncTests/SkipFamilyTests.swift`:

```swift
import XCTest
@testable import VaultSync

final class SkipFamilyTests: XCTestCase {

    // MARK: - conflictGlob

    func test_conflictGlob_rootFileWithExtension() {
        let glob = SyncthingManager.conflictGlob(forOriginalPath: "notes.md")
        XCTAssertEqual(glob, "notes.sync-conflict-*")
    }

    func test_conflictGlob_nestedFile() {
        let glob = SyncthingManager.conflictGlob(forOriginalPath: "Personal/diary.md")
        XCTAssertEqual(glob, "Personal/diary.sync-conflict-*")
    }

    func test_conflictGlob_noExtension() {
        let glob = SyncthingManager.conflictGlob(forOriginalPath: "Makefile")
        XCTAssertEqual(glob, "Makefile.sync-conflict-*")
    }

    func test_conflictGlob_filenameWithDots() {
        let glob = SyncthingManager.conflictGlob(forOriginalPath: "archive.tar.gz")
        XCTAssertEqual(glob, "archive.tar.sync-conflict-*")
    }

    // MARK: - Pair detection

    func test_pairing_recognisesPair() {
        let lines = ["notes.md", "notes.sync-conflict-*"]
        let result = SkipFamilyGrouping.group(customLines: lines)
        XCTAssertEqual(result.count, 1)
        XCTAssertEqual(result[0].original, "notes.md")
        XCTAssertTrue(result[0].hasConflictGlob)
    }

    func test_pairing_singletonRemainsSingleton() {
        let lines = ["*.tmp"]
        let result = SkipFamilyGrouping.group(customLines: lines)
        XCTAssertEqual(result.count, 1)
        XCTAssertEqual(result[0].original, "*.tmp")
        XCTAssertFalse(result[0].hasConflictGlob)
    }

    func test_pairing_orphanConflictGlobRendersAsSingle() {
        // A conflict glob with no matching original is rendered as its own row.
        let lines = ["foo.sync-conflict-*"]
        let result = SkipFamilyGrouping.group(customLines: lines)
        XCTAssertEqual(result.count, 1)
        XCTAssertEqual(result[0].original, "foo.sync-conflict-*")
        XCTAssertFalse(result[0].hasConflictGlob)
    }

    func test_pairing_mixedLines() {
        let lines = ["*.tmp", "notes.md", "notes.sync-conflict-*", "drafts/"]
        let result = SkipFamilyGrouping.group(customLines: lines).sorted { $0.original < $1.original }
        XCTAssertEqual(result.count, 3)

        XCTAssertEqual(result[0].original, "*.tmp")
        XCTAssertFalse(result[0].hasConflictGlob)

        XCTAssertEqual(result[1].original, "drafts/")
        XCTAssertFalse(result[1].hasConflictGlob)

        XCTAssertEqual(result[2].original, "notes.md")
        XCTAssertTrue(result[2].hasConflictGlob)
    }
}
```

- [ ] **Step 2: Verify the tests do not compile yet**

The references to `SyncthingManager.conflictGlob` and `SkipFamilyGrouping.group` do not exist. Run the test target build to confirm:

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/ios
xcodegen generate
xcodebuild test -project VaultSync.xcodeproj -scheme VaultSync -destination 'platform=iOS Simulator,name=iPhone 16' -only-testing:VaultSyncTests/SkipFamilyTests 2>&1 | tail -30
```

Expected: build fails with "type 'SyncthingManager' has no member 'conflictGlob'" or "cannot find 'SkipFamilyGrouping' in scope".

---

## Task 6: Implement `conflictGlob` and `SkipFamilyGrouping`

**Files:**
- Modify: `ios/VaultSync/Services/SyncthingManager.swift` (add new section near the bottom, before the closing `}` at line 1631)

- [ ] **Step 1: Add the helper and grouping type**

Append the following inside the `SyncthingManager` class body (just before the final `}` at line 1631):

```swift
    // MARK: - Skip Family

    /// Returns the `.stignore` glob that matches every Syncthing conflict copy
    /// of the given original file (relative path inside the folder).
    /// Example: "Personal/diary.md" -> "Personal/diary.sync-conflict-*".
    /// Files with no extension still work: "Makefile" -> "Makefile.sync-conflict-*".
    nonisolated static func conflictGlob(forOriginalPath originalPath: String) -> String {
        let url = URL(fileURLWithPath: originalPath)
        let ext = url.pathExtension
        let stem = ext.isEmpty ? url.lastPathComponent : url.deletingPathExtension().lastPathComponent
        let parent = url.deletingLastPathComponent().relativePath
        let glob: String
        if ext.isEmpty {
            glob = "\(stem).sync-conflict-*"
        } else {
            glob = "\(stem).sync-conflict-*"
        }
        if parent.isEmpty || parent == "." {
            return glob
        }
        return "\(parent)/\(glob)"
    }
```

Note: the `if ext.isEmpty` branches look identical because the conflict-copy glob deliberately does **not** repeat the extension. We keep the branch for symmetry with future variants (e.g. if Syncthing ever changes the conflict naming for extension-less files).

Now create the grouping type. Put it in a new section at the very end of the file, **outside** the `SyncthingManager` class:

```swift
// MARK: - Skip Family grouping

/// A logical entry in the Sync Filters "Custom patterns" list. Each entry is
/// either a singleton pattern or a paired `<X>` + `<X>.sync-conflict-*`.
struct SkipFamilyEntry: Hashable {
    /// The "presented" line — for paired entries this is the original-file
    /// pattern, for singletons it is the pattern itself.
    let original: String
    /// True iff a matching `<original-stem>.sync-conflict-*` glob also exists
    /// in `.stignore` and should be removed together with the original.
    let hasConflictGlob: Bool

    /// All `.stignore` lines this entry represents — one line for singletons,
    /// two for paired entries.
    var underlyingLines: [String] {
        if hasConflictGlob {
            return [original, SyncthingManager.conflictGlob(forOriginalPath: original)]
        }
        return [original]
    }
}

enum SkipFamilyGrouping {
    /// Group raw `.stignore` custom-section lines into Skip-Family entries.
    /// A line `X` is paired with `<dir>/<stem>.sync-conflict-*` if both exist
    /// in the input. Orphan conflict globs render as their own singleton row.
    static func group(customLines: [String]) -> [SkipFamilyEntry] {
        let lineSet = Set(customLines)
        var consumed = Set<String>()
        var result: [SkipFamilyEntry] = []

        for line in customLines {
            if consumed.contains(line) { continue }
            let glob = SyncthingManager.conflictGlob(forOriginalPath: line)
            if line != glob, lineSet.contains(glob) {
                result.append(SkipFamilyEntry(original: line, hasConflictGlob: true))
                consumed.insert(line)
                consumed.insert(glob)
            } else {
                result.append(SkipFamilyEntry(original: line, hasConflictGlob: false))
                consumed.insert(line)
            }
        }
        return result
    }
}
```

- [ ] **Step 2: Re-generate the Xcode project and run only SkipFamilyTests**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/ios
xcodegen generate
xcodebuild test -project VaultSync.xcodeproj -scheme VaultSync -destination 'platform=iOS Simulator,name=iPhone 16' -only-testing:VaultSyncTests/SkipFamilyTests 2>&1 | tail -30
```

Expected: all 7 `SkipFamilyTests` pass.

- [ ] **Step 3: Run the full test suite to catch regressions**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/ios
xcodebuild test -project VaultSync.xcodeproj -scheme VaultSync -destination 'platform=iOS Simulator,name=iPhone 16' 2>&1 | tail -30
```

Expected: all tests pass.

- [ ] **Step 4: Commit**

```bash
git add ios/VaultSync/Services/SyncthingManager.swift ios/VaultSyncTests/SkipFamilyTests.swift
git commit -m "feat(ios): add conflictGlob helper and SkipFamilyGrouping

Pure helpers shared between the conflict-resolver Skip flow and the
Sync Filters list rendering. Tests cover root files, nested paths,
extension-less filenames, mixed-extension stems, and pair detection."
```

---

## Task 7: Implement `skipFileAndCleanupConflicts`

**Files:**
- Modify: `ios/VaultSync/Services/SyncthingManager.swift` (add method in the same "Skip Family" section added in Task 6)

- [ ] **Step 1: Add the method**

In `SyncthingManager.swift`, inside the class body and right after the `conflictGlob(forOriginalPath:)` helper added in Task 6, insert:

```swift
    /// Perform the full "Always skip on this iPhone" action atomically:
    ///   1. Add both the original-path pattern and its conflict-copies glob to `.stignore`.
    ///   2. Remove every existing sync-conflict copy of the original file from disk.
    ///   3. Trigger a folder rescan so Syncthing's in-memory index reflects the changes.
    ///   4. Refresh the iOS-side conflict cache so the resolved conflict disappears.
    /// Returns:
    ///   - `error`: a user-facing error if the `.stignore` write failed; otherwise nil.
    ///   - `removedConflicts`: the number of on-disk conflict-copy files that were deleted.
    @discardableResult
    func skipFileAndCleanupConflicts(folderID: String, originalPath: String) -> (error: SyncUserError?, removedConflicts: Int) {
        let glob = Self.conflictGlob(forOriginalPath: originalPath)

        guard var current = readIgnorePatternsOrNil(folderID: folderID) else {
            return (unreadableFiltersError(), 0)
        }
        if !current.contains(originalPath) {
            current.append(originalPath)
        }
        if !current.contains(glob) {
            current.append(glob)
        }
        if let err = setIgnorePatterns(folderID: folderID, patterns: current) {
            return (err, 0)
        }

        let (removed, _) = SyncBridgeService.removeConflictFilesForOriginal(
            folderID: folderID,
            originalPath: originalPath
        )

        _ = SyncBridgeService.rescanFolder(folderID: folderID)
        refreshConflicts()

        return (nil, removed)
    }
```

- [ ] **Step 2: Build to confirm the method compiles**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/ios
xcodebuild build -project VaultSync.xcodeproj -scheme VaultSync -destination 'platform=iOS Simulator,name=iPhone 16' 2>&1 | tail -15
```

Expected: `** BUILD SUCCEEDED **`.

- [ ] **Step 3: Commit**

```bash
git add ios/VaultSync/Services/SyncthingManager.swift
git commit -m "feat(ios): add skipFileAndCleanupConflicts to SyncthingManager

Single-call API for the conflict-resolver Skip flow. Writes the pair of
ignore patterns, deletes existing conflict copies on disk, rescans, and
refreshes the conflict cache. Returns the number of removed conflict
copies so the UI can surface it in the success alert."
```

---

## Task 8: Update `ConflictDiffView.skipThisFile()`

**Files:**
- Modify: `ios/VaultSync/Views/ConflictDiffView.swift` (lines 24-28, 180-185, 196-203)

- [ ] **Step 1: Add state for the removed-count info**

Replace the existing "Always-skip flow" state block (lines 24-28):

```swift
    // Always-skip flow
    @State private var showSkipConfirmation = false
    @State private var skipErrorMessage: String?
    @State private var showSkipError = false
```

with:

```swift
    // Always-skip flow
    @State private var showSkipConfirmation = false
    @State private var skipRemovedCount: Int = 0
    @State private var skipErrorMessage: String?
    @State private var showSkipError = false
```

- [ ] **Step 2: Replace `skipThisFile()` with the new implementation**

Replace lines 196-203:

```swift
    private func skipThisFile() {
        if let err = syncthingManager.addIgnorePattern(conflict.originalPath, folderID: folderID) {
            skipErrorMessage = err.message
            showSkipError = true
            return
        }
        showSkipConfirmation = true
    }
```

with:

```swift
    private func skipThisFile() {
        let (err, removed) = syncthingManager.skipFileAndCleanupConflicts(
            folderID: folderID,
            originalPath: conflict.originalPath
        )
        if let err {
            skipErrorMessage = err.message
            showSkipError = true
            return
        }
        skipRemovedCount = removed
        showSkipConfirmation = true
    }
```

- [ ] **Step 3: Update the confirmation alert message to include the conflict-copies note**

Replace lines 180-185:

```swift
        .alert(L10n.tr("Skipping enabled"), isPresented: $showSkipConfirmation) {
            Button("OK") { showSkipConfirmation = false }
        } message: {
            Text(L10n.fmt("'%@' will no longer sync to this iPhone. You can undo this in Sync Filters.",
                          conflict.originalPath))
        }
```

with:

```swift
        .alert(L10n.tr("Skipping enabled"), isPresented: $showSkipConfirmation) {
            Button("OK") {
                showSkipConfirmation = false
                dismiss()
            }
        } message: {
            let base = L10n.fmt(
                "'%@' and its conflict copies will no longer sync to this iPhone. You can undo this in Sync Filters.",
                conflict.originalPath
            )
            if skipRemovedCount > 0 {
                Text(base + "\n\n" + L10n.fmt("%d existing conflict copies were removed.", skipRemovedCount))
            } else {
                Text(base)
            }
        }
```

The added `dismiss()` on OK closes the conflict diff view automatically — the conflict is gone, there's nothing left to show. (This makes the existing `confirmAction → executeAction → dismiss` flow consistent with the Skip flow.)

- [ ] **Step 4: Build to confirm**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/ios
xcodebuild build -project VaultSync.xcodeproj -scheme VaultSync -destination 'platform=iOS Simulator,name=iPhone 16' 2>&1 | tail -15
```

Expected: `** BUILD SUCCEEDED **`. The new format strings will produce warnings about untranslated strings — they get translated in Task 10.

- [ ] **Step 5: Commit**

```bash
git add ios/VaultSync/Views/ConflictDiffView.swift
git commit -m "feat(ios): wire ConflictDiffView Skip to skipFileAndCleanupConflicts

Replace the single-pattern addIgnorePattern call with the full Skip
flow. Confirmation alert now mentions conflict copies and, when any
existed on disk, the number that were removed. Dismisses the diff view
on OK since the conflict no longer exists."
```

---

## Task 9: Group paired patterns in `IgnorePatternsView`

**Files:**
- Modify: `ios/VaultSync/Views/IgnorePatternsView.swift` (lines 78-94, 104-109, 179-192)

- [ ] **Step 1: Replace the custom-section list rendering**

Replace the `customSection` computed property (lines 78-94):

```swift
    private var customSection: some View {
        Section(header: Text(L10n.tr("Custom patterns"))) {
            ForEach(customPatterns, id: \.self) { pattern in
                Text(pattern).font(.system(.body, design: .monospaced))
            }
            .onDelete(perform: deleteCustom)

            HStack {
                TextField(L10n.tr("Add pattern (e.g. *.tmp)"), text: $newPattern)
                    .font(.system(.body, design: .monospaced))
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                Button(L10n.tr("Add")) { addCustom() }
                    .disabled(newPattern.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
    }
```

with:

```swift
    private var customSection: some View {
        Section(header: Text(L10n.tr("Custom patterns"))) {
            ForEach(customEntries, id: \.self) { entry in
                VStack(alignment: .leading, spacing: 2) {
                    Text(entry.original)
                        .font(.system(.body, design: .monospaced))
                    if entry.hasConflictGlob {
                        Text(L10n.tr("+ conflict copies"))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            .onDelete(perform: deleteCustom)

            HStack {
                TextField(L10n.tr("Add pattern (e.g. *.tmp)"), text: $newPattern)
                    .font(.system(.body, design: .monospaced))
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                Button(L10n.tr("Add")) { addCustom() }
                    .disabled(newPattern.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
    }
```

- [ ] **Step 2: Replace the derived `customPatterns` with `customEntries`**

Replace lines 104-109:

```swift
    private var customPatterns: [String] {
        let presetPatterns = Set(IgnorePreset.all.flatMap(\.patterns))
        return ignoredPatterns
            .filter { !presetPatterns.contains($0) }
            .sorted()
    }
```

with:

```swift
    private var customEntries: [SkipFamilyEntry] {
        let presetPatterns = Set(IgnorePreset.all.flatMap(\.patterns))
        let lines = ignoredPatterns
            .filter { !presetPatterns.contains($0) }
            .sorted()
        return SkipFamilyGrouping.group(customLines: lines)
    }
```

- [ ] **Step 3: Replace `deleteCustom` to operate on grouped entries**

Replace lines 179-192:

```swift
    private func deleteCustom(at offsets: IndexSet) {
        let visible = customPatterns
        let toRemove = offsets.compactMap { index -> String? in
            guard index < visible.count else { return nil }
            return visible[index]
        }
        var next = Array(ignoredPatterns)
        next.removeAll { toRemove.contains($0) }
        if let err = syncthingManager.setIgnorePatterns(folderID: folderID, patterns: next) {
            alertMessage = err.message
            return
        }
        reloadPatterns()
    }
```

with:

```swift
    private func deleteCustom(at offsets: IndexSet) {
        let visible = customEntries
        let toRemoveLines: [String] = offsets.flatMap { index -> [String] in
            guard index < visible.count else { return [] }
            return visible[index].underlyingLines
        }
        let toRemoveSet = Set(toRemoveLines)
        var next = Array(ignoredPatterns)
        next.removeAll { toRemoveSet.contains($0) }
        if let err = syncthingManager.setIgnorePatterns(folderID: folderID, patterns: next) {
            alertMessage = err.message
            return
        }
        reloadPatterns()
    }
```

- [ ] **Step 4: Build and run the existing test suite**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/ios
xcodebuild test -project VaultSync.xcodeproj -scheme VaultSync -destination 'platform=iOS Simulator,name=iPhone 16' 2>&1 | tail -20
```

Expected: all tests pass, including the new `SkipFamilyTests` and the existing `IgnorePresetTests`/`SyncFiltersTests`.

- [ ] **Step 5: Commit**

```bash
git add ios/VaultSync/Views/IgnorePatternsView.swift
git commit -m "feat(ios): group Skip-Family pairs as one row in Sync Filters

A paired '<X>' + '<X>.sync-conflict-*' renders as a single Custom
Patterns entry with a '+ conflict copies' caption. Swipe-to-delete
removes both lines from .stignore atomically. Orphan singletons keep
their existing single-line rendering."
```

---

## Task 10: Localization strings

**Files:**
- Modify: `ios/VaultSync/en.lproj/Localizable.strings` (after line 479)
- Modify: `ios/VaultSync/de.lproj/Localizable.strings` (after the corresponding line)
- Modify: `ios/VaultSync/zh-Hans.lproj/Localizable.strings` (after the corresponding line)

- [ ] **Step 1: Update the English strings file**

In `ios/VaultSync/en.lproj/Localizable.strings`, replace the line at line 479:

```
"'%@' will no longer sync to this iPhone. You can undo this in Sync Filters." = "'%@' will no longer sync to this iPhone. You can undo this in Sync Filters.";
```

with:

```
"'%@' will no longer sync to this iPhone. You can undo this in Sync Filters." = "'%@' will no longer sync to this iPhone. You can undo this in Sync Filters.";
"'%@' and its conflict copies will no longer sync to this iPhone. You can undo this in Sync Filters." = "'%@' and its conflict copies will no longer sync to this iPhone. You can undo this in Sync Filters.";
"%d existing conflict copies were removed." = "%d existing conflict copies were removed.";
"+ conflict copies" = "+ conflict copies";
```

The old key is intentionally kept (other call sites may still reference it; removal is out of scope for this fix).

- [ ] **Step 2: Update the German strings file**

In `ios/VaultSync/de.lproj/Localizable.strings`, after the existing line:

```
"'%@' will no longer sync to this iPhone. You can undo this in Sync Filters." = "„%@" wird nicht mehr auf dieses iPhone synchronisiert. Du kannst das in den Sync-Filtern rückgängig machen.";
```

append:

```
"'%@' and its conflict copies will no longer sync to this iPhone. You can undo this in Sync Filters." = "„%@" und seine Konflikt-Kopien werden nicht mehr auf dieses iPhone synchronisiert. Du kannst das in den Sync-Filtern rückgängig machen.";
"%d existing conflict copies were removed." = "%d vorhandene Konflikt-Kopien wurden entfernt.";
"+ conflict copies" = "+ Konflikt-Kopien";
```

- [ ] **Step 3: Update the Simplified Chinese strings file**

In `ios/VaultSync/zh-Hans.lproj/Localizable.strings`, after the existing line:

```
"'%@' will no longer sync to this iPhone. You can undo this in Sync Filters." = "「%@」将不再同步到此 iPhone。你可以在同步过滤器中撤销。";
```

append:

```
"'%@' and its conflict copies will no longer sync to this iPhone. You can undo this in Sync Filters." = "「%@」及其冲突副本将不再同步到此 iPhone。你可以在同步过滤器中撤销。";
"%d existing conflict copies were removed." = "已移除 %d 个现有冲突副本。";
"+ conflict copies" = "+ 冲突副本";
```

- [ ] **Step 4: Build and test once more to confirm everything still passes**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/ios
xcodebuild test -project VaultSync.xcodeproj -scheme VaultSync -destination 'platform=iOS Simulator,name=iPhone 16' 2>&1 | tail -20
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add ios/VaultSync/en.lproj/Localizable.strings ios/VaultSync/de.lproj/Localizable.strings ios/VaultSync/zh-Hans.lproj/Localizable.strings
git commit -m "feat(ios): localize Skip-Family alert and Sync Filters caption

Adds the new 'conflict copies' alert variant, the removed-count
sentence, and the '+ conflict copies' caption used by the Sync Filters
list, for en / de / zh-Hans."
```

---

## Task 11: Version bump

**Files:**
- Modify: `ios/project.yml` (lines 53-54 and 103-104)

- [ ] **Step 1: Bump app version**

In `ios/project.yml`, change lines 53-54 from:

```yaml
        CFBundleShortVersionString: "1.3.1"
        CFBundleVersion: "23"
```

to:

```yaml
        CFBundleShortVersionString: "1.3.2"
        CFBundleVersion: "24"
```

- [ ] **Step 2: Bump widget version**

In the same file, change lines 103-104 from:

```yaml
        CFBundleShortVersionString: "1.3.1"
        CFBundleVersion: "23"
```

to:

```yaml
        CFBundleShortVersionString: "1.3.2"
        CFBundleVersion: "24"
```

- [ ] **Step 3: Regenerate the Xcode project and verify the new versions land in Info.plist**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/ios
xcodegen generate
grep -A1 "CFBundleShortVersionString" VaultSync/Info.plist
```

Expected: `<string>1.3.2</string>` shows up in the matched output.

- [ ] **Step 4: Commit**

```bash
git add ios/project.yml ios/VaultSync/Info.plist ios/VaultSyncWidget/Info.plist
git commit -m "chore: bump to 1.3.2 (build 24)"
```

(Note: the two `Info.plist` files are gitignored per `.gitignore` lines 5-7, so `git add` will silently skip them. The `project.yml` change is what counts; the build re-generates Info.plist locally.)

---

## Task 12: CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md` (insert new section at the top, before the `## [1.3.1]` line at line 7)

- [ ] **Step 1: Insert the 1.3.2 section**

In `CHANGELOG.md`, replace the lines starting at line 5 (the `---` separator and the `## [1.3.1]` header) by inserting the new section *above* the existing 1.3.1 section. The result should look like:

```markdown
---

## [1.3.2] — 2026-05-23

### Fixed

- **Skip on iPhone now actually skips returning conflicts** ([#8](https://github.com/psimaker/vaultsync/issues/8)) — Tapping "Always skip on this iPhone" in the conflict resolver previously added only the original file's path to `.stignore`, so a fresh `sync-conflict-…` copy with a new timestamp would arrive from the desktop and the conflict reappeared. The Skip flow now also writes a `<path>.sync-conflict-*` glob, deletes any conflict copies of the file already sitting in the vault, and rescans so the conflict disappears from the Sync Issues list immediately. The Sync Filters → Custom Patterns list groups the pair as a single row with a "+ conflict copies" caption.

---

## [1.3.1] — 2026-05-17
```

The existing `## [1.3.1]` content stays unchanged below the new section.

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog entry for 1.3.2 (skip-family fix, #8)"
```

---

## Task 13: Update the Sync Filters UX spec

**Files:**
- Modify: `docs/sync-filters-ux.md` (lines 117-130 — §6 "Conflict → Ignore")

- [ ] **Step 1: Rewrite §6 with the new family behavior**

Replace lines 117-130:

```markdown
## 6. Conflict → Ignore

In `ConflictDiffView`, a new toolbar menu appears (top-right `⋯`):

```
⋯ menu
└─ Always skip on this iPhone
```

Tapping it adds the conflict's *exact relative path* to the folder's ignore list. Then a confirmation alert:

> "`'.obsidian/plugins/dataview/cache.db'` will no longer sync to this iPhone. You can undo this in Sync Filters."

Reasoning behind exact-path (not smart-glob): predictable. The user knows exactly what they ignored. If they later want to widen to `*.cache.db` or `.obsidian/plugins/dataview/*`, they can do that in the editor.
```

with:

```markdown
## 6. Conflict → Ignore

In `ConflictDiffView`, a toolbar menu appears (top-right `⋯`):

```
⋯ menu
└─ Always skip on this iPhone
```

Tapping it performs a **Skip Family** action (added in v1.3.2, see issue [#8](https://github.com/psimaker/vaultsync/issues/8)):

1. Writes a *pair* of patterns to `.stignore`: the file's exact relative path and a matching `<path>.sync-conflict-*` glob.
2. Deletes any sync-conflict copies of that file currently on disk.
3. Rescans the folder and refreshes the conflict cache so the conflict disappears from the home-screen Sync Issues list immediately.

Confirmation alert:

> "`'.obsidian/plugins/dataview/cache.db'` and its conflict copies will no longer sync to this iPhone. You can undo this in Sync Filters."

If existing conflict copies were removed, a second line is appended:

> "2 existing conflict copies were removed."

Reasoning behind the family approach: the v1.2.0 design used an exact-path pattern for predictability, but that left a hole — a fresh `sync-conflict-…` copy with a new timestamp would arrive from the desktop and the conflict reappeared. Pairing the original path with the conflict-copy glob makes "skip" actually mean skip, without sacrificing predictability: the two `.stignore` lines are still plain, no smart-glob heuristics, no hidden state. In the Sync Filters list the pair is presented as a single row with a `+ conflict copies` caption.

The original file itself is **not** deleted from disk — only the conflict-copy variants. Users who later want to revert can swipe-to-delete the row in Sync Filters; both lines are removed atomically.
```

- [ ] **Step 2: Update the "Last updated" line at the top**

In the same file, change line 4 from:

```markdown
> Last updated: 2026-05-09
```

to:

```markdown
> Last updated: 2026-05-23
```

- [ ] **Step 3: Commit**

```bash
git add docs/sync-filters-ux.md
git commit -m "docs: update sync-filters UX spec for skip-family (#8)"
```

---

## Task 14: Final verification

- [ ] **Step 1: Run Go tests one more time**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/go
go test -tags noassets ./bridge/
```

Expected: all tests pass.

- [ ] **Step 2: Run the full iOS test suite**

```bash
cd /Users/anon/Nextcloud/Projekte/vaultsync/ios
xcodebuild test -project VaultSync.xcodeproj -scheme VaultSync -destination 'platform=iOS Simulator,name=iPhone 16' 2>&1 | tail -30
```

Expected: 0 failing tests. All targets `VaultSync`, `VaultSyncWidget`, `VaultSyncTests` build cleanly.

- [ ] **Step 3: Manual smoke test (simulator)**

Boot the simulator and run the app. Reproduce vitaly74's scenario:

1. Add a test vault with a single file `notes.md`.
2. From a peer (or by manually creating it), introduce a conflict file `notes.sync-conflict-20260523-100000-AAA1111.md`.
3. In VaultSync, open the conflict in the diff view. Tap ⋯ → "Always skip on this iPhone".
4. Verify the alert reads "'notes.md' and its conflict copies will no longer sync to this iPhone… 1 existing conflict copies were removed."
5. Tap OK — view dismisses, conflict no longer appears in Sync Issues.
6. Open Sync Filters → Custom Patterns. Verify a single grouped row with "notes.md" and "+ conflict copies" caption.
7. Force a fresh divergence: create another `notes.sync-conflict-20260523-110000-BBB2222.md` on disk under the vault folder. Trigger a rescan. The conflict must **not** reappear in the Sync Issues list.
8. In Sync Filters, swipe-delete the "notes.md (+ conflict copies)" row. Verify both `.stignore` lines are gone (use the relay-diagnostics or a manual `.stignore` check) and that the previously-suppressed conflict can now resurface on the next divergence.

Document any failure as a follow-up task before moving on.

- [ ] **Step 4: Show the full commit log on this branch**

```bash
git log --oneline main..HEAD
```

Expected: roughly 8 commits (one per implementation task), all on `fix/issue-8-conflict-skip-family`, none with a `Co-Authored-By:` trailer.

- [ ] **Step 5: Hand off to the user**

Branch is ready for the user to review and merge to `main`. **Do not push, do not merge** — the user handles both.

---

## Self-Review Notes

- **Spec coverage:** Every section of `2026-05-23-conflict-skip-family-design.md` is implemented:
  - §4.1–§4.2 (Skip Family + glob): Tasks 5–6
  - §4.3 (write path): Tasks 7–8
  - §4.4 (read/render path): Task 9
  - §4.5 (Go bridge): Tasks 1–4
  - §4.6 (alert copy): Tasks 8 + 10
  - §6 (edge cases) covered by Task 1 tests (root, nested, idempotency, traversal) and Task 5 tests (no-extension, dotted stem, pair / singleton / orphan)
  - §7 (tests): Tasks 1, 5, 14
  - §8 (UX spec update): Task 13
  - §10 (rollout): Tasks 11–12
- **Placeholder scan:** No "TBD", "TODO", "implement later", or hand-wave error-handling phrases remain.
- **Type consistency:** `SyncthingManager.conflictGlob(forOriginalPath:)`, `SkipFamilyEntry.original`, `SkipFamilyEntry.hasConflictGlob`, `SkipFamilyEntry.underlyingLines`, `SkipFamilyGrouping.group(customLines:)`, `SyncthingManager.skipFileAndCleanupConflicts(folderID:originalPath:)`, and `SyncBridgeService.removeConflictFilesForOriginal(folderID:originalPath:)` are used identically in every task that references them.
