package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/h2cone/serpe/core/models"
	"github.com/h2cone/serpe/internal/jsonvalue"
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
	maxMaintenanceIdentityBytes  = 256
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

type migrationManifest struct {
	Format              string                   `json:"format"`
	SourceSchemaVersion int                      `json:"source_schema_version"`
	TargetStoreFormat   string                   `json:"target_store_format"`
	StoreRoot           string                   `json:"store_root"`
	RootIdentity        string                   `json:"root_identity"`
	BackupIdentity      string                   `json:"backup_identity"`
	CWDBase             string                   `json:"cwd_base"`
	Entries             []migrationManifestEntry `json:"entries"`
}

type migrationManifestEntry struct {
	ID             string `json:"id"`
	SourceName     string `json:"source_name"`
	TargetName     string `json:"target_name"`
	BackupName     string `json:"backup_name"`
	SourceIdentity string `json:"source_identity"`
	SourceBytes    int64  `json:"source_bytes"`
	SourceSHA256   string `json:"source_sha256"`
	TargetBytes    int64  `json:"target_bytes"`
	TargetSHA256   string `json:"target_sha256"`
	CWDChanged     bool   `json:"cwd_changed"`
}

type maintenanceRoot struct {
	path       string
	file       *os.File
	handle     *os.Root
	identity   string
	lock       *os.File
	unlock     func() error
	removeLock bool
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
	root, err := openMaintenanceRoot(options.StoreRoot, mode == "dry-run")
	if err != nil {
		return MaintenanceResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, root.close()) }()
	result = MaintenanceResult{Mode: mode, StoreRoot: root.path}

	switch mode {
	case "dry-run", "apply":
		manifest, actions, err := scanLegacyStore(ctx, root, options.CWDBase, options.Limits)
		result.Actions = actions
		if err != nil {
			return result, err
		}
		if mode == "dry-run" {
			return result, nil
		}
		manifest, manifestPath, err := createMigrationBackup(ctx, root, manifest, options.Limits)
		result.Manifest = manifestPath
		result.RestoreCommand = maintenanceCommand(root.path, "--restore", manifestPath)
		result.CleanupCommand = maintenanceCommand(root.path, "--cleanup", manifestPath)
		if err != nil {
			return result, err
		}
		if err := applyMigration(ctx, root, manifest, manifestPath, options.Limits); err != nil {
			return result, fmt.Errorf("migration stopped; restore with %s: %w", result.RestoreCommand, err)
		}
		return result, nil
	case "restore":
		manifest, manifestPath, err := loadMigrationManifest(ctx, root, options.RestoreManifest, options.Limits)
		result.Manifest = manifestPath
		if err != nil {
			return result, err
		}
		result.Actions = manifestActions(manifest)
		if err := restoreMigration(ctx, root, manifest, manifestPath, options.Limits); err != nil {
			return result, err
		}
		return result, nil
	case "cleanup":
		manifest, manifestPath, err := loadMigrationManifest(ctx, root, options.CleanupManifest, options.Limits)
		result.Manifest = manifestPath
		if err != nil {
			return result, err
		}
		result.Actions = manifestActions(manifest)
		if err := cleanupMigrationBackup(ctx, root, manifest, manifestPath, options.Limits); err != nil {
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

func openMaintenanceRoot(path string, removeNewLock bool) (*maintenanceRoot, error) {
	listed, err := os.Lstat(path)
	if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.IsDir() {
		return nil, fmt.Errorf("%w: maintenance root is not a non-symlink directory", ErrInvalidSession)
	}
	rootFile, err := openStoreDirectory(path)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = rootFile.Close()
		}
	}()
	info, err := rootFile.Stat()
	if err != nil || validateStoreRoot(rootFile, info) != nil {
		return nil, fmt.Errorf("%w: maintenance root ownership or permissions are unsafe", ErrInvalidSession)
	}
	identity, err := storeFileIdentity(rootFile)
	if err != nil {
		return nil, err
	}
	if identity == "" || len(identity) > maxMaintenanceIdentityBytes {
		return nil, fmt.Errorf("%w: maintenance root identity is invalid", ErrStoreCorrupt)
	}
	rootHandle, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if failed {
			_ = rootHandle.Close()
		}
	}()
	rootView, err := openRootDirectory(rootHandle, ".")
	if err != nil {
		return nil, err
	}
	rootViewIdentity, identityErr := storeFileIdentity(rootView)
	closeErr := rootView.Close()
	if identityErr != nil || closeErr != nil || rootViewIdentity != identity {
		return nil, fmt.Errorf("%w: rooted maintenance namespace identity mismatch", ErrStoreCorrupt)
	}
	_, beforeErr := rootHandle.Lstat(storeLockName)
	lockWasMissing := os.IsNotExist(beforeErr)
	lockFile, err := openStoreLock(rootHandle)
	if err != nil {
		return nil, err
	}
	defer func() {
		if failed {
			_ = lockFile.Close()
		}
	}()
	unlock, err := lockStoreFile(lockFile)
	if err != nil {
		return nil, fmt.Errorf("%w: store is open or cannot be locked", ErrInvalidSession)
	}
	failed = false
	return &maintenanceRoot{path: path, file: rootFile, handle: rootHandle, identity: identity, lock: lockFile, unlock: unlock, removeLock: removeNewLock && lockWasMissing}, nil
}

func (root *maintenanceRoot) close() error {
	if root == nil {
		return nil
	}
	var errs []error
	// A dry-run removes a lock file that it had to create. Unlink it while
	// the old file is still locked: unlocking first would let another opener
	// acquire this directory entry just before we remove it.
	if root.removeLock && root.handle != nil {
		errs = append(errs, root.handle.Remove(storeLockName))
		if root.file != nil {
			errs = append(errs, syncStoreDirectory(root.file))
		}
		root.removeLock = false
	}
	if root.unlock != nil {
		errs = append(errs, root.unlock())
		root.unlock = nil
	}
	if root.lock != nil {
		errs = append(errs, root.lock.Close())
		root.lock = nil
	}
	if root.handle != nil {
		errs = append(errs, root.handle.Close())
		root.handle = nil
	}
	if root.file != nil {
		errs = append(errs, root.file.Close())
		root.file = nil
	}
	return errors.Join(errs...)
}

func (root *maintenanceRoot) check() error {
	listed, err := os.Lstat(root.path)
	if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.IsDir() {
		return fmt.Errorf("%w: maintenance root changed", ErrStoreCorrupt)
	}
	current, err := openStoreDirectory(root.path)
	if err != nil {
		return fmt.Errorf("%w: maintenance root changed", ErrStoreCorrupt)
	}
	info, statErr := current.Stat()
	identity, identityErr := storeFileIdentity(current)
	validationErr := validateStoreRoot(current, info)
	closeErr := current.Close()
	if statErr != nil || identityErr != nil || validationErr != nil || closeErr != nil || identity != root.identity {
		return fmt.Errorf("%w: maintenance root identity changed", ErrStoreCorrupt)
	}
	return nil
}

func scanLegacyStore(ctx context.Context, root *maintenanceRoot, cwdBase string, limits MaintenanceLimits) (migrationManifest, []MaintenanceAction, error) {
	if err := root.check(); err != nil {
		return migrationManifest{}, nil, err
	}
	if err := requireNoFormatMarker(root.handle); err != nil {
		return migrationManifest{}, nil, err
	}
	entries, err := readRootDirEntries(root.handle, ".")
	if err != nil {
		return migrationManifest{}, nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	manifest := migrationManifest{
		Format: maintenanceManifestFormat, SourceSchemaVersion: schemaVersion,
		TargetStoreFormat: storeFormatV2, StoreRoot: root.path,
		RootIdentity: root.identity, CWDBase: cwdBase,
	}
	seenIDs := make(map[string]struct{})
	seenTargets := make(map[string]struct{})
	var total int64
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return migrationManifest{}, nil, err
		}
		name := entry.Name()
		if !isJSONCandidate(name) {
			continue
		}
		if !strings.HasSuffix(name, recordNameSuffixV2) {
			return migrationManifest{}, nil, fmt.Errorf("%w: non-lowercase legacy JSON suffix %q", ErrStoreCorrupt, name)
		}
		if strings.HasPrefix(name, recordNamePrefixV2) {
			return migrationManifest{}, nil, fmt.Errorf("%w: unmarked v2 record %q", ErrStoreCorrupt, name)
		}
		if len(manifest.Entries) >= limits.MaxRecords {
			return migrationManifest{}, nil, fmt.Errorf("%w: legacy record count exceeds %d", ErrRecordTooLarge, limits.MaxRecords)
		}
		id := strings.TrimSuffix(name, recordNameSuffixV2)
		if !validID(id) {
			return migrationManifest{}, nil, fmt.Errorf("%w: legacy filename %q has an invalid ID", ErrStoreCorrupt, name)
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return migrationManifest{}, nil, fmt.Errorf("%w: duplicate legacy ID %q", ErrStoreCorrupt, id)
		}
		seenIDs[id] = struct{}{}
		target := encodeRecordName(id)
		if _, duplicate := seenTargets[target]; duplicate {
			return migrationManifest{}, nil, fmt.Errorf("%w: duplicate migration target", ErrStoreCorrupt)
		}
		seenTargets[target] = struct{}{}
		if _, err := root.handle.Lstat(target); err == nil {
			return migrationManifest{}, nil, fmt.Errorf("%w: migration target %q already exists", ErrStoreCorrupt, target)
		} else if !os.IsNotExist(err) {
			return migrationManifest{}, nil, err
		}
		data, identity, err := readMaintenanceRecord(ctx, root, name, limits.MaxRecordBytes)
		if err != nil {
			return migrationManifest{}, nil, err
		}
		if identity == "" || len(identity) > maxMaintenanceIdentityBytes {
			return migrationManifest{}, nil, fmt.Errorf("%w: source identity is invalid", ErrStoreCorrupt)
		}
		if int64(len(data)) > limits.MaxTotalBytes-total {
			return migrationManifest{}, nil, fmt.Errorf("%w: legacy source bytes exceed %d", ErrRecordTooLarge, limits.MaxTotalBytes)
		}
		total += int64(len(data))
		migrated, cwdChanged, err := migrateRecord(data, id, cwdBase)
		if err != nil {
			return migrationManifest{}, nil, fmt.Errorf("legacy record %q: %w", name, err)
		}
		if int64(len(migrated)) > limits.MaxRecordBytes {
			return migrationManifest{}, nil, fmt.Errorf("%w: migrated record %q exceeds %d bytes", ErrRecordTooLarge, name, limits.MaxRecordBytes)
		}
		sourceDigest := sha256.Sum256(data)
		targetDigest := sha256.Sum256(migrated)
		index := len(manifest.Entries)
		manifest.Entries = append(manifest.Entries, migrationManifestEntry{
			ID: id, SourceName: name, TargetName: target,
			BackupName:     fmt.Sprintf("b_%05d%s", index, maintenanceBackupSuffix),
			SourceIdentity: identity, SourceBytes: int64(len(data)), SourceSHA256: hex.EncodeToString(sourceDigest[:]),
			TargetBytes: int64(len(migrated)), TargetSHA256: hex.EncodeToString(targetDigest[:]), CWDChanged: cwdChanged,
		})
	}
	// BackupIdentity is learned only after the directory is created. Reserve a
	// conservative bounded identity before any backup write so a manifest cap
	// can never fail for the first time after backup creation.
	manifest.BackupIdentity = strings.Repeat("x", maxMaintenanceIdentityBytes)
	manifestBytes, err := encodeMigrationManifest(manifest)
	if err != nil {
		return migrationManifest{}, nil, err
	}
	if int64(len(manifestBytes)) > limits.MaxManifestBytes {
		return migrationManifest{}, nil, fmt.Errorf("%w: migration manifest exceeds %d bytes", ErrRecordTooLarge, limits.MaxManifestBytes)
	}
	manifest.BackupIdentity = ""
	return manifest, manifestActions(manifest), nil
}

func requireNoFormatMarker(root *os.Root) error {
	if _, err := root.Lstat(storeFormatName); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("%w: format marker already exists", ErrStoreCorrupt)
}

func readMaintenanceRecord(ctx context.Context, root *maintenanceRoot, name string, limit int64) ([]byte, string, error) {
	if filepath.Base(name) != name {
		return nil, "", fmt.Errorf("%w: invalid maintenance record name", ErrStoreCorrupt)
	}
	listed, err := root.handle.Lstat(name)
	if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%w: unsafe maintenance record %q", ErrStoreCorrupt, name)
	}
	if listed.Size() < 0 || listed.Size() > limit {
		return nil, "", fmt.Errorf("%w: record %q exceeds %d bytes", ErrRecordTooLarge, name, limit)
	}
	file, err := openRootRegular(root.handle, name, os.O_RDONLY)
	if err != nil {
		return nil, "", err
	}
	info, statErr := file.Stat()
	identity, identityErr := storeFileIdentity(file)
	validationErr := validateStoreRegular(file, info)
	if statErr != nil || identityErr != nil || validationErr != nil {
		_ = file.Close()
		return nil, "", fmt.Errorf("%w: unsafe maintenance record %q", ErrStoreCorrupt, name)
	}
	data, readErr := readBoundedContext(ctx, file, limit)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, "", errors.Join(readErr, closeErr)
	}
	if int64(len(data)) != info.Size() {
		return nil, "", fmt.Errorf("%w: record %q changed while reading", ErrStoreCorrupt, name)
	}
	return data, identity, nil
}

func readBoundedContext(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	if limit < 1 {
		return nil, fmt.Errorf("invalid read limit")
	}
	buffer := bytes.NewBuffer(make([]byte, 0, minInt64(limit, 64<<10)))
	chunk := make([]byte, 64<<10)
	var total int64
	for {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		n, err := reader.Read(chunk)
		if n > 0 {
			if int64(n) > limit-total {
				return nil, fmt.Errorf("%w: bounded record read overflow", ErrRecordTooLarge)
			}
			total += int64(n)
			_, _ = buffer.Write(chunk[:n])
		}
		if errors.Is(err, io.EOF) {
			return buffer.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func minInt64(left, right int64) int {
	if left < right {
		return int(left)
	}
	return int(right)
}

func migrateRecord(data []byte, filenameID, cwdBase string) ([]byte, bool, error) {
	if _, err := jsonvalue.Parse(data, jsonvalue.Limits{MaxDepth: 128, MaxNodes: 1_048_576, MaxNumberBytes: 128, MaxExponent: 1_000, MaxScale: 1_024}); err != nil {
		return nil, false, fmt.Errorf("%w: record is not strict JSON: %v", ErrInvalidSession, err)
	}
	var raw sessionRecord
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false, fmt.Errorf("%w: decode legacy record: %v", ErrInvalidSession, err)
	}
	if raw.SchemaVersion != schemaVersion || raw.ID != filenameID {
		return nil, false, fmt.Errorf("%w: filename ID and record ID/schema do not match", ErrInvalidSession)
	}
	cwdChanged := false
	if !filepath.IsAbs(raw.CWD) {
		if raw.CWD == "" || filepath.VolumeName(raw.CWD) != "" {
			return nil, false, fmt.Errorf("%w: legacy CWD is not a portable relative path", ErrInvalidSession)
		}
		raw.CWD = filepath.Clean(filepath.Join(cwdBase, raw.CWD))
		cwdChanged = true
	}
	session := &Session{ID: raw.ID, ParentID: raw.ParentID, CWD: raw.CWD, CreatedAt: raw.CreatedAt.UTC(), UpdatedAt: raw.UpdatedAt.UTC(), Messages: make([]models.Message, len(raw.Messages))}
	for index := range raw.Messages {
		message, err := decodeMessage(raw.Messages[index])
		if err != nil {
			return nil, false, fmt.Errorf("%w: message %d: %v", ErrInvalidSession, index, err)
		}
		session.Messages[index] = message
	}
	if len(raw.Metadata) > 0 {
		session.Metadata = make(map[string]string, len(raw.Metadata))
		for key, value := range raw.Metadata {
			session.Metadata[key] = value
		}
	}
	if err := session.Validate(); err != nil {
		return nil, false, err
	}
	migrated, err := marshalSession(session)
	return migrated, cwdChanged, err
}

func encodeMigrationManifest(manifest migrationManifest) ([]byte, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode migration manifest: %w", err)
	}
	return data, nil
}

func createMigrationBackup(ctx context.Context, root *maintenanceRoot, manifest migrationManifest, limits MaintenanceLimits) (migrationManifest, string, error) {
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
	manifestPath := filepath.Join(root.path, backupName, maintenanceManifestName)
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
	if err := syncStoreDirectory(root.file); err != nil {
		return migrationManifest{}, "", fmt.Errorf("%w: backup directory sync: %v", ErrCommitUncertain, err)
	}
	return manifest, manifestPath, nil
}

func createBackupDirectory(root *maintenanceRoot) (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		var random [16]byte
		if _, err := io.ReadFull(fileStoreRandom, random[:]); err != nil {
			return "", fmt.Errorf("generate backup name: %w", err)
		}
		name := maintenanceBackupPrefix + hex.EncodeToString(random[:])
		err := root.handle.Mkdir(name, 0o700)
		if err == nil {
			return name, nil
		}
		if !os.IsExist(err) || attempt == 3 {
			return "", err
		}
	}
	return "", fmt.Errorf("backup directory attempts exhausted")
}

func writeExclusiveSynced(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written := 0
	for written < len(data) {
		n, writeErr := file.Write(data[written:])
		if writeErr != nil || n == 0 {
			_ = file.Close()
			return errors.Join(writeErr, io.ErrShortWrite)
		}
		written += n
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func applyMigration(ctx context.Context, root *maintenanceRoot, manifest migrationManifest, manifestPath string, limits MaintenanceLimits) error {
	if err := verifyBackup(ctx, root, manifest, manifestPath, limits); err != nil {
		return err
	}
	store := &FileStore{root: root.path, rootFile: root.file, rootHandle: root.handle, rootIdentity: root.identity}
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
		if err := store.atomicWrite(ctx, entry.ID, migrated, true); err != nil {
			return err
		}
		if err := root.handle.Remove(entry.SourceName); err != nil {
			return fmt.Errorf("%w: remove legacy source %q: %v", ErrCommitUncertain, entry.SourceName, err)
		}
		if err := syncStoreDirectory(root.file); err != nil {
			return fmt.Errorf("%w: sync migrated source removal: %v", ErrCommitUncertain, err)
		}
	}
	return store.publishMarker()
}

func loadMigrationManifest(ctx context.Context, root *maintenanceRoot, requested string, limits MaintenanceLimits) (migrationManifest, string, error) {
	path, err := validateManifestPath(root.path, requested)
	if err != nil {
		return migrationManifest{}, "", err
	}
	backupName := filepath.Base(filepath.Dir(path))
	backup, backupIdentity, err := openBackupRoot(root, backupName)
	if err != nil {
		return migrationManifest{}, "", err
	}
	data, _, err := readMaintenanceFile(ctx, backup, maintenanceManifestName, limits.MaxManifestBytes)
	closeErr := backup.Close()
	if err != nil {
		return migrationManifest{}, "", err
	}
	if closeErr != nil {
		return migrationManifest{}, "", closeErr
	}
	if _, err := jsonvalue.Parse(data, jsonvalue.Limits{MaxDepth: 64, MaxNodes: 1_048_576, MaxNumberBytes: 128, MaxExponent: 1_000, MaxScale: 1_024}); err != nil {
		return migrationManifest{}, "", fmt.Errorf("%w: manifest is not strict JSON", ErrStoreCorrupt)
	}
	var manifest migrationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return migrationManifest{}, "", fmt.Errorf("%w: invalid migration manifest", ErrStoreCorrupt)
	}
	if manifest.Format != maintenanceManifestFormat || manifest.SourceSchemaVersion != schemaVersion ||
		manifest.TargetStoreFormat != storeFormatV2 || filepath.Clean(manifest.StoreRoot) != root.path ||
		manifest.RootIdentity != root.identity || manifest.BackupIdentity != backupIdentity || !filepath.IsAbs(manifest.CWDBase) {
		return migrationManifest{}, "", fmt.Errorf("%w: manifest does not belong to this store", ErrStoreCorrupt)
	}
	if len(manifest.Entries) > limits.MaxRecords {
		return migrationManifest{}, "", fmt.Errorf("%w: manifest record count exceeds limit", ErrRecordTooLarge)
	}
	if encoded, err := encodeMigrationManifest(manifest); err != nil || !bytes.Equal(encoded, data) {
		return migrationManifest{}, "", fmt.Errorf("%w: manifest is not canonical", ErrStoreCorrupt)
	}
	if err := validateManifestEntries(manifest, limits); err != nil {
		return migrationManifest{}, "", err
	}
	return manifest, path, nil
}

func validateManifestPath(root, requested string) (string, error) {
	clean := filepath.Clean(requested)
	if !filepath.IsAbs(clean) || filepath.Base(clean) != maintenanceManifestName {
		return "", fmt.Errorf("%w: manifest path must be an absolute manifest.json", ErrInvalidSession)
	}
	backup := filepath.Dir(clean)
	if filepath.Dir(backup) != root || !isBackupDirectoryName(filepath.Base(backup)) {
		return "", fmt.Errorf("%w: manifest must be in a direct store backup child", ErrInvalidSession)
	}
	return clean, nil
}

func isBackupDirectoryName(name string) bool {
	if !strings.HasPrefix(name, maintenanceBackupPrefix) {
		return false
	}
	suffix := strings.TrimPrefix(name, maintenanceBackupPrefix)
	if len(suffix) != 32 || suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func openBackupRoot(root *maintenanceRoot, name string) (*os.Root, string, error) {
	if root == nil || root.handle == nil || filepath.Base(name) != name || !isBackupDirectoryName(name) {
		return nil, "", fmt.Errorf("%w: backup directory name is unsafe", ErrStoreCorrupt)
	}
	directory, err := openRootDirectory(root.handle, name)
	if err != nil {
		return nil, "", err
	}
	identity, identityErr := storeFileIdentity(directory)
	closeErr := directory.Close()
	if identityErr != nil || closeErr != nil || identity == "" || len(identity) > maxMaintenanceIdentityBytes {
		return nil, "", fmt.Errorf("%w: backup directory ownership or permissions are unsafe", ErrStoreCorrupt)
	}
	backup, err := root.handle.OpenRoot(name)
	if err != nil {
		return nil, "", err
	}
	view, err := openRootDirectory(backup, ".")
	if err != nil {
		_ = backup.Close()
		return nil, "", err
	}
	viewIdentity, identityErr := storeFileIdentity(view)
	closeErr = view.Close()
	if identityErr != nil || closeErr != nil || viewIdentity != identity {
		_ = backup.Close()
		return nil, "", fmt.Errorf("%w: rooted backup identity mismatch", ErrStoreCorrupt)
	}
	return backup, identity, nil
}

func validateManifestEntries(manifest migrationManifest, limits MaintenanceLimits) error {
	seenIDs := make(map[string]struct{}, len(manifest.Entries))
	seenNames := make(map[string]struct{}, len(manifest.Entries)*3)
	var total int64
	for index, entry := range manifest.Entries {
		if !validID(entry.ID) || entry.SourceName != entry.ID+recordNameSuffixV2 || entry.TargetName != encodeRecordName(entry.ID) ||
			entry.BackupName != fmt.Sprintf("b_%05d%s", index, maintenanceBackupSuffix) || entry.SourceBytes < 0 || entry.SourceBytes > limits.MaxRecordBytes ||
			entry.TargetBytes < 0 || entry.TargetBytes > limits.MaxRecordBytes || !validSHA256Hex(entry.SourceSHA256) || !validSHA256Hex(entry.TargetSHA256) ||
			entry.SourceIdentity == "" || len(entry.SourceIdentity) > maxMaintenanceIdentityBytes || manifest.BackupIdentity == "" || len(manifest.BackupIdentity) > maxMaintenanceIdentityBytes {
			return fmt.Errorf("%w: invalid migration manifest entry", ErrStoreCorrupt)
		}
		if _, duplicate := seenIDs[entry.ID]; duplicate {
			return fmt.Errorf("%w: duplicate manifest ID", ErrStoreCorrupt)
		}
		seenIDs[entry.ID] = struct{}{}
		for _, name := range []string{entry.SourceName, entry.TargetName, entry.BackupName} {
			if filepath.Base(name) != name {
				return fmt.Errorf("%w: unsafe manifest filename", ErrStoreCorrupt)
			}
			key := name
			if _, duplicate := seenNames[key]; duplicate {
				return fmt.Errorf("%w: duplicate manifest filename", ErrStoreCorrupt)
			}
			seenNames[key] = struct{}{}
		}
		if entry.SourceBytes > limits.MaxTotalBytes-total {
			return fmt.Errorf("%w: manifest source bytes exceed total limit", ErrRecordTooLarge)
		}
		total += entry.SourceBytes
	}
	return nil
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func verifyBackup(ctx context.Context, root *maintenanceRoot, manifest migrationManifest, manifestPath string, limits MaintenanceLimits) error {
	backupName := filepath.Base(filepath.Dir(manifestPath))
	backup, identity, err := openBackupRoot(root, backupName)
	if err != nil || identity != manifest.BackupIdentity {
		return errors.Join(err, fmt.Errorf("%w: backup directory identity changed", ErrStoreCorrupt))
	}
	defer func() { _ = backup.Close() }()
	wanted := map[string]struct{}{maintenanceManifestName: {}}
	for _, entry := range manifest.Entries {
		wanted[entry.BackupName] = struct{}{}
		data, _, err := readMaintenanceFile(ctx, backup, entry.BackupName, limits.MaxRecordBytes)
		if err != nil || int64(len(data)) != entry.SourceBytes || sha256Hex(data) != entry.SourceSHA256 {
			return errors.Join(err, fmt.Errorf("%w: backup %q failed checksum verification", ErrStoreCorrupt, entry.BackupName))
		}
	}
	entries, err := readRootDirEntries(backup, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, ok := wanted[entry.Name()]; !ok || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: backup contains an unknown or unsafe entry", ErrStoreCorrupt)
		}
	}
	return nil
}

func readMaintenanceFile(ctx context.Context, root *os.Root, name string, limit int64) ([]byte, string, error) {
	if root == nil || filepath.Base(name) != name {
		return nil, "", fmt.Errorf("%w: unsafe maintenance filename", ErrStoreCorrupt)
	}
	listed, err := root.Lstat(name)
	if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() || listed.Size() < 0 || listed.Size() > limit {
		return nil, "", fmt.Errorf("%w: unsafe or oversized maintenance file", ErrStoreCorrupt)
	}
	file, err := openRootRegular(root, name, os.O_RDONLY)
	if err != nil {
		return nil, "", err
	}
	info, statErr := file.Stat()
	identity, identityErr := storeFileIdentity(file)
	validationErr := validateStoreRegular(file, info)
	if statErr != nil || identityErr != nil || validationErr != nil {
		_ = file.Close()
		return nil, "", fmt.Errorf("%w: unsafe maintenance file", ErrStoreCorrupt)
	}
	data, readErr := readBoundedContext(ctx, file, limit)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) != info.Size() {
		return nil, "", errors.Join(readErr, closeErr, fmt.Errorf("%w: maintenance file changed", ErrStoreCorrupt))
	}
	return data, identity, nil
}

func restoreMigration(ctx context.Context, root *maintenanceRoot, manifest migrationManifest, manifestPath string, limits MaintenanceLimits) error {
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
		if err := root.handle.Remove(storeFormatName); err != nil {
			return err
		}
		if err := syncStoreDirectory(root.file); err != nil {
			return fmt.Errorf("%w: remove migration marker: %v", ErrCommitUncertain, err)
		}
	}
	store := &FileStore{root: root.path, rootFile: root.file, rootHandle: root.handle, rootIdentity: root.identity}
	for _, entry := range manifest.Entries {
		if _, err := root.handle.Lstat(entry.SourceName); os.IsNotExist(err) {
			data, _, readErr := readMaintenanceFile(ctx, backup, entry.BackupName, limits.MaxRecordBytes)
			if readErr != nil {
				return readErr
			}
			if err := atomicPublishName(ctx, store, entry.SourceName, data); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := root.handle.Lstat(entry.TargetName); err == nil {
			if err := root.handle.Remove(entry.TargetName); err != nil {
				return err
			}
			if err := syncStoreDirectory(root.file); err != nil {
				return fmt.Errorf("%w: restore target removal: %v", ErrCommitUncertain, err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func validateOptionalMarker(ctx context.Context, root *maintenanceRoot) (bool, error) {
	listed, err := root.handle.Lstat(storeFormatName)
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

func validateRestoreState(ctx context.Context, root *maintenanceRoot, entry migrationManifestEntry, limits MaintenanceLimits) error {
	sourceExists := false
	if _, err := root.handle.Lstat(entry.SourceName); err == nil {
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
	if _, err := root.handle.Lstat(entry.TargetName); err == nil {
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

func atomicPublishName(ctx context.Context, store *FileStore, destinationName string, data []byte) error {
	tempName, file, err := store.createTemp(destinationName + ".")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = store.rootHandle.Remove(tempName)
		}
	}()
	for written := 0; written < len(data); {
		if err := contextError(ctx); err != nil {
			_ = file.Close()
			return err
		}
		n, writeErr := file.Write(data[written:])
		if writeErr != nil || n == 0 {
			_ = file.Close()
			return errors.Join(writeErr, io.ErrShortWrite)
		}
		written += n
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := store.rootHandle.Link(tempName, destinationName); err != nil {
		return err
	}
	committed = true
	if err := store.rootHandle.Remove(tempName); err != nil {
		return fmt.Errorf("%w: remove restore temp: %v", ErrCommitUncertain, err)
	}
	if err := syncStoreDirectory(store.rootFile); err != nil {
		return fmt.Errorf("%w: sync restored record: %v", ErrCommitUncertain, err)
	}
	return nil
}

func cleanupMigrationBackup(ctx context.Context, root *maintenanceRoot, manifest migrationManifest, manifestPath string, limits MaintenanceLimits) error {
	if err := verifyBackup(ctx, root, manifest, manifestPath, limits); err != nil {
		return err
	}
	marker, err := validateOptionalMarker(ctx, root)
	if err != nil || !marker {
		return errors.Join(err, fmt.Errorf("%w: cleanup requires a completed v2 migration", ErrStoreCorrupt))
	}
	for _, entry := range manifest.Entries {
		if _, err := root.handle.Lstat(entry.SourceName); !os.IsNotExist(err) {
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
	if err := root.handle.Remove(backupName); err != nil {
		return fmt.Errorf("%w: backup directory is not empty or cannot be removed: %v", ErrStoreCorrupt, err)
	}
	if err := syncStoreDirectory(root.file); err != nil {
		return fmt.Errorf("%w: cleanup directory sync: %v", ErrCommitUncertain, err)
	}
	return nil
}

func manifestActions(manifest migrationManifest) []MaintenanceAction {
	actions := make([]MaintenanceAction, len(manifest.Entries))
	for index, entry := range manifest.Entries {
		actions[index] = MaintenanceAction{ID: entry.ID, SourceName: entry.SourceName, TargetName: entry.TargetName, CWDChanged: entry.CWDChanged, Bytes: entry.SourceBytes}
	}
	return actions
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func maintenanceCommand(root, flag, manifest string) string {
	return fmt.Sprintf("serpe-server migrate-store --store-root %q %s %q", root, flag, manifest)
}
