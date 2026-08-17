package sessions

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func createMigrationBackup(ctx context.Context, root *storeRoot, manifest migrationManifest, limits MaintenanceLimits) (migrationManifest, string, error) {
	backupName, err := createBackupDirectory(root)
	if err != nil {
		return migrationManifest{}, "", err
	}
	backup, identity, err := openBackupRoot(root, backupName)
	if err != nil {
		return migrationManifest{}, "", err
	}
	directory, err := openRootDirectory(backup, ".")
	if err != nil {
		_ = backup.Close()
		return migrationManifest{}, "", err
	}
	manifest.BackupIdentity = identity
	manifestData, err := encodeMigrationManifest(manifest)
	if err != nil || int64(len(manifestData)) > limits.MaxManifestBytes {
		_ = directory.Close()
		_ = backup.Close()
		return migrationManifest{}, "", errors.Join(err, fmt.Errorf("%w: migration manifest exceeds limit", ErrRecordTooLarge))
	}
	for _, entry := range manifest.Entries {
		if err := contextError(ctx); err != nil {
			_ = directory.Close()
			_ = backup.Close()
			return migrationManifest{}, "", err
		}
		data, identity, err := readMaintenanceRecord(ctx, root, entry.SourceName, limits.MaxRecordBytes)
		if err != nil {
			_ = directory.Close()
			_ = backup.Close()
			return migrationManifest{}, "", err
		}
		if identity != entry.SourceIdentity || int64(len(data)) != entry.SourceBytes || sha256Hex(data) != entry.SourceSHA256 {
			_ = directory.Close()
			_ = backup.Close()
			return migrationManifest{}, "", fmt.Errorf("%w: source %q changed before backup", ErrStoreCorrupt, entry.SourceName)
		}
		if err := writeExclusiveSynced(backup, entry.BackupName, data); err != nil {
			_ = directory.Close()
			_ = backup.Close()
			return migrationManifest{}, "", err
		}
	}
	manifestPath := filepath.Join(root.root, backupName, maintenanceManifestName)
	if err := writeExclusiveSynced(backup, maintenanceManifestName, manifestData); err != nil {
		_ = directory.Close()
		_ = backup.Close()
		return migrationManifest{}, "", err
	}
	syncErr := syncStoreDirectory(directory)
	closeErr := directory.Close()
	rootCloseErr := backup.Close()
	if syncErr != nil || closeErr != nil || rootCloseErr != nil {
		return migrationManifest{}, "", errors.Join(syncErr, closeErr, rootCloseErr)
	}
	if err := syncStoreDirectory(root.rootFile); err != nil {
		return migrationManifest{}, "", fmt.Errorf("%w: backup directory sync: %v", ErrCommitUncertain, err)
	}
	return manifest, manifestPath, nil
}

func createBackupDirectory(root *storeRoot) (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		var random [16]byte
		if _, err := io.ReadFull(fileStoreRandom, random[:]); err != nil {
			return "", fmt.Errorf("generate backup name: %w", err)
		}
		name := maintenanceBackupPrefix + hex.EncodeToString(random[:])
		err := root.rootHandle.Mkdir(name, 0o700)
		if err == nil {
			return name, nil
		}
		if !os.IsExist(err) || attempt == 3 {
			return "", err
		}
	}
	return "", fmt.Errorf("backup directory attempts exhausted")
}

func applyMigration(ctx context.Context, root *storeRoot, manifest migrationManifest, manifestPath string, limits MaintenanceLimits) error {
	if err := verifyBackup(ctx, root, manifest, manifestPath, limits); err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		if err := root.check(); err != nil {
			return err
		}
		data, identity, err := readMaintenanceRecord(ctx, root, entry.SourceName, limits.MaxRecordBytes)
		if err != nil {
			return err
		}
		if identity != entry.SourceIdentity || int64(len(data)) != entry.SourceBytes || sha256Hex(data) != entry.SourceSHA256 {
			return fmt.Errorf("%w: source %q changed after backup", ErrStoreCorrupt, entry.SourceName)
		}
		migrated, _, err := migrateRecord(data, entry.ID, manifest.CWDBase)
		if err != nil || int64(len(migrated)) != entry.TargetBytes || sha256Hex(migrated) != entry.TargetSHA256 {
			return errors.Join(err, fmt.Errorf("%w: migrated record %q no longer matches manifest", ErrStoreCorrupt, entry.SourceName))
		}
		if err := root.publishExclusive(ctx, entry.TargetName, migrated); err != nil {
			return err
		}
		if err := root.rootHandle.Remove(entry.SourceName); err != nil {
			return fmt.Errorf("%w: remove legacy source %q: %v", ErrCommitUncertain, entry.SourceName, err)
		}
		if err := root.syncDir(); err != nil {
			return fmt.Errorf("%w: sync migrated source removal: %v", ErrCommitUncertain, err)
		}
	}
	return root.publishMarker()
}
