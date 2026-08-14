package sessions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

const (
	maintenanceManifestName   = "manifest.json"
	maintenanceManifestFormat = "serpe.sessions.migration.v1"
	maintenanceBackupPrefix   = ".serpe-store-backup-"
	maintenanceBackupSuffix   = ".record"

	defaultMigrationRecords      = 65_536
	defaultMigrationRecordBytes  = int64(256 << 20)
	defaultMigrationTotalBytes   = int64(1 << 40)
	defaultMigrationManifestSize = int64(64 << 20)
)

// MaintenanceLimits may only tighten the offline migration hard ceilings.
// Zero fields select the package ceilings.
type MaintenanceLimits struct {
	MaxRecords       int
	MaxRecordBytes   int64
	MaxTotalBytes    int64
	MaxManifestBytes int64
}

// MaintenanceOptions selects exactly one offline FileStore operation. With
// neither RestoreManifest nor CleanupManifest set, Apply=false is a dry-run
// and Apply=true performs the migration after creating a verified backup.
type MaintenanceOptions struct {
	StoreRoot       string
	CWDBase         string
	Apply           bool
	RestoreManifest string
	CleanupManifest string
	Limits          MaintenanceLimits
}

// MaintenanceAction is one planned or completed legacy-record conversion.
type MaintenanceAction struct {
	ID         string `json:"id"`
	SourceName string `json:"source_name"`
	TargetName string `json:"target_name"`
	CWDChanged bool   `json:"cwd_changed"`
	Bytes      int64  `json:"bytes"`
}

// MaintenanceResult is a bounded, payload-free report suitable for a CLI.
type MaintenanceResult struct {
	Mode           string              `json:"mode"`
	StoreRoot      string              `json:"store_root"`
	Actions        []MaintenanceAction `json:"actions,omitempty"`
	Manifest       string              `json:"manifest,omitempty"`
	RestoreCommand string              `json:"restore_command,omitempty"`
	CleanupCommand string              `json:"cleanup_command,omitempty"`
}

// MaintainFileStore executes one explicitly selected offline maintenance
// operation while holding the same cross-process lock as FileStore.
func MaintainFileStore(ctx context.Context, options MaintenanceOptions) (result MaintenanceResult, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	mode, err := validateMaintenanceOptions(&options)
	if err != nil {
		return MaintenanceResult{}, err
	}
	root, err := openStoreRoot(options.StoreRoot, storeRootOptions{removeNewLock: mode == "dry-run"})
	if err != nil {
		return MaintenanceResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, root.close()) }()
	result = MaintenanceResult{Mode: mode, StoreRoot: root.root}

	switch mode {
	case "dry-run", "apply":
		manifest, actions, err := scanLegacyStore(ctx, &root, options.CWDBase, options.Limits)
		result.Actions = actions
		if err != nil {
			return result, err
		}
		if mode == "dry-run" {
			return result, nil
		}
		manifest, manifestPath, err := createMigrationBackup(ctx, &root, manifest, options.Limits)
		result.Manifest = manifestPath
		result.RestoreCommand = maintenanceCommand(root.root, "--restore", manifestPath)
		result.CleanupCommand = maintenanceCommand(root.root, "--cleanup", manifestPath)
		if err != nil {
			return result, err
		}
		if err := applyMigration(ctx, &root, manifest, manifestPath, options.Limits); err != nil {
			return result, fmt.Errorf("migration stopped; restore with %s: %w", result.RestoreCommand, err)
		}
		return result, nil
	case "restore":
		manifest, manifestPath, err := loadMigrationManifest(ctx, &root, options.RestoreManifest, options.Limits)
		result.Manifest = manifestPath
		if err != nil {
			return result, err
		}
		result.Actions = manifestActions(manifest)
		if err := restoreMigration(ctx, &root, manifest, manifestPath, options.Limits); err != nil {
			return result, err
		}
		return result, nil
	case "cleanup":
		manifest, manifestPath, err := loadMigrationManifest(ctx, &root, options.CleanupManifest, options.Limits)
		result.Manifest = manifestPath
		if err != nil {
			return result, err
		}
		result.Actions = manifestActions(manifest)
		if err := cleanupMigrationBackup(ctx, &root, manifest, manifestPath, options.Limits); err != nil {
			return result, err
		}
		return result, nil
	default:
		return result, fmt.Errorf("%w: unknown maintenance mode", ErrInvalidSession)
	}
}

func validateMaintenanceOptions(options *MaintenanceOptions) (string, error) {
	if options == nil || options.StoreRoot == "" || !filepath.IsAbs(options.StoreRoot) {
		return "", fmt.Errorf("%w: --store-root must be absolute", ErrInvalidSession)
	}
	options.StoreRoot = filepath.Clean(options.StoreRoot)
	modes := 0
	mode := "dry-run"
	if options.Apply {
		modes++
		mode = "apply"
	}
	if options.RestoreManifest != "" {
		modes++
		mode = "restore"
	}
	if options.CleanupManifest != "" {
		modes++
		mode = "cleanup"
	}
	if modes > 1 {
		return "", fmt.Errorf("%w: apply, restore, and cleanup are mutually exclusive", ErrInvalidSession)
	}
	if mode == "dry-run" || mode == "apply" {
		if options.CWDBase == "" || !filepath.IsAbs(options.CWDBase) {
			return "", fmt.Errorf("%w: --cwd-base must be absolute for migration", ErrInvalidSession)
		}
		options.CWDBase = filepath.Clean(options.CWDBase)
	} else {
		if options.CWDBase != "" {
			return "", fmt.Errorf("%w: --cwd-base is not valid with restore or cleanup", ErrInvalidSession)
		}
		manifestPath := options.RestoreManifest
		if mode == "cleanup" {
			manifestPath = options.CleanupManifest
		}
		if !filepath.IsAbs(manifestPath) {
			return "", fmt.Errorf("%w: manifest path must be absolute", ErrInvalidSession)
		}
	}
	limits, err := normalizeMaintenanceLimits(options.Limits)
	if err != nil {
		return "", err
	}
	options.Limits = limits
	return mode, nil
}

func normalizeMaintenanceLimits(limits MaintenanceLimits) (MaintenanceLimits, error) {
	if limits.MaxRecords < 0 || limits.MaxRecordBytes < 0 || limits.MaxTotalBytes < 0 || limits.MaxManifestBytes < 0 {
		return MaintenanceLimits{}, fmt.Errorf("%w: maintenance limits must not be negative", ErrInvalidSession)
	}
	if limits.MaxRecords == 0 {
		limits.MaxRecords = defaultMigrationRecords
	}
	if limits.MaxRecordBytes == 0 {
		limits.MaxRecordBytes = defaultMigrationRecordBytes
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = defaultMigrationTotalBytes
	}
	if limits.MaxManifestBytes == 0 {
		limits.MaxManifestBytes = defaultMigrationManifestSize
	}
	if limits.MaxRecords > defaultMigrationRecords || limits.MaxRecordBytes > defaultMigrationRecordBytes ||
		limits.MaxTotalBytes > defaultMigrationTotalBytes || limits.MaxManifestBytes > defaultMigrationManifestSize ||
		limits.MaxRecords < 1 || limits.MaxRecordBytes < 1 || limits.MaxTotalBytes < 1 || limits.MaxManifestBytes < 1 {
		return MaintenanceLimits{}, fmt.Errorf("%w: maintenance limit exceeds its package ceiling", ErrInvalidSession)
	}
	return limits, nil
}

func maintenanceCommand(root, flag, manifest string) string {
	return fmt.Sprintf("serpe-server migrate-store --store-root %q %s %q", root, flag, manifest)
}
