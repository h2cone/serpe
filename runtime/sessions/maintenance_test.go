package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFileStoreMaintenanceDryRunApplyRestore(t *testing.T) {
	root := privateTempDir(t)
	base := filepath.Join(root, "workspace-base")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyRecord(t, root, "Case.ID", filepath.Join("relative", "cwd"))
	before := directoryNames(t, root)

	dry, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CWDBase: base})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Mode != "dry-run" || len(dry.Actions) != 1 || !dry.Actions[0].CWDChanged {
		t.Fatalf("dry-run result = %+v", dry)
	}
	if after := directoryNames(t, root); !slices.Equal(before, after) {
		t.Fatalf("dry-run changed directory: before=%v after=%v", before, after)
	}

	applied, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CWDBase: base, Apply: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Manifest == "" || applied.RestoreCommand == "" || applied.CleanupCommand == "" {
		t.Fatalf("apply result = %+v", applied)
	}
	if _, err := os.Lstat(filepath.Join(root, "Case.ID.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy source still exists: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, encodeRecordName("Case.ID"))); err != nil {
		t.Fatal(err)
	} else if session, err := unmarshalSession(data); err != nil {
		t.Fatalf("migrated record: %v", err)
	} else if want := filepath.Join(base, "relative", "cwd"); session.CWD != want {
		t.Fatalf("CWD = %q, want %q", session.CWD, want)
	}

	restored, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, RestoreManifest: applied.Manifest})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.Mode != "restore" {
		t.Fatalf("restore result = %+v", restored)
	}
	if _, err := os.Lstat(filepath.Join(root, "Case.ID.json")); err != nil {
		t.Fatalf("restored legacy source: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, encodeRecordName("Case.ID"))); !os.IsNotExist(err) {
		t.Fatalf("v2 target remains after restore: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, storeFormatName)); !os.IsNotExist(err) {
		t.Fatalf("marker remains after restore: %v", err)
	}
}

func TestFileStoreMaintenanceCleanupRejectsUnknownBackupEntry(t *testing.T) {
	root := privateTempDir(t)
	base := filepath.Join(root, "base")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyRecord(t, root, "one", base)
	applied, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CWDBase: base, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(filepath.Dir(applied.Manifest), "unknown")
	if err := os.WriteFile(extra, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CleanupManifest: applied.Manifest}); err == nil {
		t.Fatal("cleanup accepted an unknown backup entry")
	}
	if data, err := os.ReadFile(extra); err != nil || string(data) != "do not delete" {
		t.Fatalf("unknown backup entry was changed: %q, %v", data, err)
	}
	if _, err := os.Lstat(applied.Manifest); err != nil {
		t.Fatalf("manifest removed on rejected cleanup: %v", err)
	}
}

func TestFileStoreMaintenanceCleanupSuccess(t *testing.T) {
	root := privateTempDir(t)
	base := filepath.Join(root, "base")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyRecord(t, root, "clean", base)
	applied, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CWDBase: base, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Dir(applied.Manifest)
	if _, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CleanupManifest: applied.Manifest}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(backupDir); !os.IsNotExist(err) {
		t.Fatalf("backup remains after cleanup: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, encodeRecordName("clean"))); err != nil {
		t.Fatalf("cleanup changed migrated target: %v", err)
	}
}

func TestFileStoreMaintenanceRejectsBackupIdentityTamper(t *testing.T) {
	root := privateTempDir(t)
	base := filepath.Join(root, "base")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyRecord(t, root, "identity", base)
	applied, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CWDBase: base, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(applied.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	var manifest migrationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.BackupIdentity = "tampered:v1"
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(applied.Manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CleanupManifest: applied.Manifest}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("cleanup after identity tamper = %v, want ErrStoreCorrupt", err)
	}
}

func TestFileStoreMaintenanceLimitsFailBeforeBackup(t *testing.T) {
	for _, test := range []struct {
		name   string
		limits func(sourceBytes int64) MaintenanceLimits
		count  int
	}{
		{"record count", func(int64) MaintenanceLimits { return MaintenanceLimits{MaxRecords: 1} }, 2},
		{"record bytes", func(size int64) MaintenanceLimits { return MaintenanceLimits{MaxRecordBytes: size - 1} }, 1},
		{"total bytes", func(size int64) MaintenanceLimits { return MaintenanceLimits{MaxTotalBytes: size - 1} }, 1},
		{"manifest bytes", func(int64) MaintenanceLimits { return MaintenanceLimits{MaxManifestBytes: 1} }, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := privateTempDir(t)
			base := filepath.Join(root, "base")
			if err := os.Mkdir(base, 0o700); err != nil {
				t.Fatal(err)
			}
			for index := 0; index < test.count; index++ {
				writeLegacyRecord(t, root, fmt.Sprintf("limit-%d", index), base)
			}
			first, err := os.ReadFile(filepath.Join(root, "limit-0.json"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = MaintainFileStore(context.Background(), MaintenanceOptions{
				StoreRoot: root, CWDBase: base, Apply: true, Limits: test.limits(int64(len(first))),
			})
			if !errors.Is(err, ErrRecordTooLarge) {
				t.Fatalf("MaintainFileStore = %v, want ErrRecordTooLarge", err)
			}
			assertNoMaintenanceBackup(t, root)
		})
	}
}

func TestFileStoreMaintenanceRejectsExpandedTargetBeforeBackup(t *testing.T) {
	root := privateTempDir(t)
	base := filepath.Join(root, strings.Repeat("long-base-", 8))
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyRecord(t, root, "expanded", "relative")
	source, err := os.ReadFile(filepath.Join(root, "expanded.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = MaintainFileStore(context.Background(), MaintenanceOptions{
		StoreRoot: root, CWDBase: base, Apply: true,
		Limits: MaintenanceLimits{MaxRecordBytes: int64(len(source))},
	})
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("expanded migration = %v, want ErrRecordTooLarge", err)
	}
	assertNoMaintenanceBackup(t, root)
}

func TestFileStoreMaintenanceBackupEntropyAndCollisions(t *testing.T) {
	t.Run("entropy failure", func(t *testing.T) {
		root, base := maintenanceFixture(t, "entropy")
		previous := fileStoreRandom
		fileStoreRandom = alwaysFailReader{err: errors.New("entropy unavailable")}
		defer func() { fileStoreRandom = previous }()
		if _, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CWDBase: base, Apply: true}); err == nil {
			t.Fatal("migration succeeded without backup entropy")
		}
		assertNoMaintenanceBackup(t, root)
		if _, err := os.Lstat(filepath.Join(root, "entropy.json")); err != nil {
			t.Fatalf("source changed after entropy failure: %v", err)
		}
	})

	for _, test := range []struct {
		name       string
		collisions int
		wantOK     bool
	}{{"three collisions", 3, true}, {"four collisions", 4, false}} {
		t.Run(test.name, func(t *testing.T) {
			root, base := maintenanceFixture(t, "collision")
			var random bytes.Buffer
			for attempt := 0; attempt < 8; attempt++ {
				block := bytes.Repeat([]byte{byte(attempt + 1)}, 16)
				random.Write(block)
				if attempt < test.collisions {
					name := maintenanceBackupPrefix + fmt.Sprintf("%x", block)
					if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			previous := fileStoreRandom
			fileStoreRandom = &random
			defer func() { fileStoreRandom = previous }()
			result, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CWDBase: base, Apply: true})
			if test.wantOK && err != nil {
				t.Fatalf("migration after three collisions: %v", err)
			}
			if !test.wantOK && err == nil {
				t.Fatalf("migration after four collisions succeeded: %+v", result)
			}
			if test.wantOK {
				if _, err := os.Lstat(filepath.Join(root, encodeRecordName("collision"))); err != nil {
					t.Fatalf("migrated target missing: %v", err)
				}
			} else {
				if _, err := os.Lstat(filepath.Join(root, "collision.json")); err != nil {
					t.Fatalf("source changed after exhausted collisions: %v", err)
				}
				if _, err := os.Lstat(filepath.Join(root, encodeRecordName("collision"))); !os.IsNotExist(err) {
					t.Fatalf("target appeared after exhausted collisions: %v", err)
				}
			}
		})
	}
}

func TestFileStoreMaintenancePreservesCaseDistinctIDs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows filesystem fixture cannot portably create A.json and a.json")
	}
	root := privateTempDir(t)
	base := filepath.Join(root, "base")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyRecord(t, root, "A", base)
	writeLegacyRecord(t, root, "a", base)
	result, err := MaintainFileStore(context.Background(), MaintenanceOptions{StoreRoot: root, CWDBase: base, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 2 || result.Actions[0].TargetName == result.Actions[1].TargetName {
		t.Fatalf("actions = %+v", result.Actions)
	}
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	restrictTestStoreDir(t, root)
	absolute, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func writeLegacyRecord(t *testing.T, root, id, cwd string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := sessionRecord{SchemaVersion: schemaVersion, ID: id, CWD: cwd, CreatedAt: now, UpdatedAt: now, Messages: []recordMessage{}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, id+recordNameSuffixV2), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func directoryNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for index := range entries {
		names[index] = entries[index].Name()
	}
	slices.Sort(names)
	return names
}

func maintenanceFixture(t *testing.T, id string) (string, string) {
	t.Helper()
	root := privateTempDir(t)
	base := filepath.Join(root, "base")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyRecord(t, root, id, base)
	return root, base
}

func assertNoMaintenanceBackup(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), maintenanceBackupPrefix) {
			t.Fatalf("backup was created before validation completed: %q", entry.Name())
		}
	}
}
