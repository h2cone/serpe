package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func restoreMigration(ctx context.Context, root *storeRoot, manifest migrationManifest, manifestPath string, limits MaintenanceLimits) error {
	if err := verifyBackup(ctx, root, manifest, manifestPath, limits); err != nil {
		return err
	}
	backupName := filepath.Base(filepath.Dir(manifestPath))
	backup, identity, err := openBackupRoot(root, backupName)
	if err != nil || identity != manifest.BackupIdentity {
		return errors.Join(err, fmt.Errorf("%w: backup directory identity changed", ErrStoreCorrupt))
	}
	defer func() { _ = backup.Close() }()
	markerExists, err := validateOptionalMarker(ctx, root)
	if err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		if err := validateRestoreState(ctx, root, entry, limits); err != nil {
			return err
		}
	}
	if markerExists {
		if err := root.rootHandle.Remove(storeFormatName); err != nil {
			return err
		}
		if err := root.syncDir(); err != nil {
			return fmt.Errorf("%w: remove migration marker: %v", ErrCommitUncertain, err)
		}
	}
	for _, entry := range manifest.Entries {
		if _, err := root.rootHandle.Lstat(entry.SourceName); os.IsNotExist(err) {
			data, _, readErr := readMaintenanceFile(ctx, backup, entry.BackupName, limits.MaxRecordBytes)
			if readErr != nil {
				return readErr
			}
			if err := root.publishExclusive(ctx, entry.SourceName, data); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := root.rootHandle.Lstat(entry.TargetName); err == nil {
			if err := root.rootHandle.Remove(entry.TargetName); err != nil {
				return err
			}
			if err := root.syncDir(); err != nil {
				return fmt.Errorf("%w: restore target removal: %v", ErrCommitUncertain, err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func validateOptionalMarker(ctx context.Context, root *storeRoot) (bool, error) {
	listed, err := root.rootHandle.Lstat(storeFormatName)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() {
		return false, fmt.Errorf("%w: unsafe migration marker", ErrStoreCorrupt)
	}
	data, _, readErr := readMaintenanceRecord(ctx, root, storeFormatName, int64(len(storeFormatV2))+1)
	if readErr != nil || string(data) != storeFormatV2 {
		return false, fmt.Errorf("%w: contradictory migration marker", ErrStoreCorrupt)
	}
	return true, nil
}

func validateRestoreState(ctx context.Context, root *storeRoot, entry migrationManifestEntry, limits MaintenanceLimits) error {
	sourceExists := false
	if _, err := root.rootHandle.Lstat(entry.SourceName); err == nil {
		data, _, readErr := readMaintenanceRecord(ctx, root, entry.SourceName, limits.MaxRecordBytes)
		if readErr != nil {
			return readErr
		}
		sourceExists = true
		if int64(len(data)) != entry.SourceBytes || sha256Hex(data) != entry.SourceSHA256 {
			return fmt.Errorf("%w: legacy source %q was externally modified", ErrStoreCorrupt, entry.SourceName)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	targetExists := false
	if _, err := root.rootHandle.Lstat(entry.TargetName); err == nil {
		data, _, readErr := readMaintenanceRecord(ctx, root, entry.TargetName, limits.MaxRecordBytes)
		if readErr != nil {
			return readErr
		}
		targetExists = true
		if int64(len(data)) != entry.TargetBytes || sha256Hex(data) != entry.TargetSHA256 {
			return fmt.Errorf("%w: migration target %q was externally modified", ErrStoreCorrupt, entry.TargetName)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if !sourceExists && !targetExists {
		return fmt.Errorf("%w: both source and target are missing for %q", ErrStoreCorrupt, entry.ID)
	}
	return nil
}

func cleanupMigrationBackup(ctx context.Context, root *storeRoot, manifest migrationManifest, manifestPath string, limits MaintenanceLimits) error {
	if err := verifyBackup(ctx, root, manifest, manifestPath, limits); err != nil {
		return err
	}
	marker, err := validateOptionalMarker(ctx, root)
	if err != nil || !marker {
		return errors.Join(err, fmt.Errorf("%w: cleanup requires a completed v2 migration", ErrStoreCorrupt))
	}
	for _, entry := range manifest.Entries {
		if _, err := root.rootHandle.Lstat(entry.SourceName); !os.IsNotExist(err) {
			return fmt.Errorf("%w: cleanup found a remaining legacy source", ErrStoreCorrupt)
		}
		data, _, err := readMaintenanceRecord(ctx, root, entry.TargetName, limits.MaxRecordBytes)
		if err != nil || int64(len(data)) != entry.TargetBytes || sha256Hex(data) != entry.TargetSHA256 {
			return errors.Join(err, fmt.Errorf("%w: cleanup target verification failed", ErrStoreCorrupt))
		}
	}
	backupName := filepath.Base(filepath.Dir(manifestPath))
	backup, identity, err := openBackupRoot(root, backupName)
	if err != nil || identity != manifest.BackupIdentity {
		return errors.Join(err, fmt.Errorf("%w: backup directory identity changed", ErrStoreCorrupt))
	}
	for _, entry := range manifest.Entries {
		if err := backup.Remove(entry.BackupName); err != nil {
			_ = backup.Close()
			return err
		}
	}
	if err := backup.Remove(maintenanceManifestName); err != nil {
		_ = backup.Close()
		return err
	}
	if err := backup.Close(); err != nil {
		return err
	}
	if err := root.rootHandle.Remove(backupName); err != nil {
		return fmt.Errorf("%w: backup directory is not empty or cannot be removed: %v", ErrStoreCorrupt, err)
	}
	if err := root.syncDir(); err != nil {
		return fmt.Errorf("%w: cleanup directory sync: %v", ErrCommitUncertain, err)
	}
	return nil
}
