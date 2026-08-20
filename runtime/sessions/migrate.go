package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/h2cone/serpe/internal/jsonvalue"
)

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

func scanLegacyStore(ctx context.Context, root *storeRoot, cwdBase string, limits MaintenanceLimits) (migrationManifest, []MaintenanceAction, error) {
	if err := root.check(); err != nil {
		return migrationManifest{}, nil, err
	}
	if err := requireNoFormatMarker(root.rootHandle); err != nil {
		return migrationManifest{}, nil, err
	}
	entries, err := readRootDirEntries(root.rootHandle, ".")
	if err != nil {
		return migrationManifest{}, nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	manifest := migrationManifest{
		Format: maintenanceManifestFormat, SourceSchemaVersion: schemaVersion,
		TargetStoreFormat: storeFormatV2, StoreRoot: root.root,
		RootIdentity: root.rootIdentity, CWDBase: cwdBase,
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
		if _, err := root.rootHandle.Lstat(target); err == nil {
			return migrationManifest{}, nil, fmt.Errorf("%w: migration target %q already exists", ErrStoreCorrupt, target)
		} else if !os.IsNotExist(err) {
			return migrationManifest{}, nil, err
		}
		data, identity, err := readMaintenanceRecord(ctx, root, name, limits.MaxRecordBytes)
		if err != nil {
			return migrationManifest{}, nil, err
		}
		if identity == "" || len(identity) > maxStoreIdentityBytes {
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
	manifest.BackupIdentity = strings.Repeat("x", maxStoreIdentityBytes)
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

func readMaintenanceRecord(ctx context.Context, root *storeRoot, name string, limit int64) ([]byte, string, error) {
	if filepath.Base(name) != name {
		return nil, "", fmt.Errorf("%w: invalid maintenance record name", ErrStoreCorrupt)
	}
	listed, err := root.rootHandle.Lstat(name)
	if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%w: unsafe maintenance record %q", ErrStoreCorrupt, name)
	}
	if listed.Size() < 0 || listed.Size() > limit {
		return nil, "", fmt.Errorf("%w: record %q exceeds %d bytes", ErrRecordTooLarge, name, limit)
	}
	file, err := openRootRegular(root.rootHandle, name, os.O_RDONLY)
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

func encodeMigrationManifest(manifest migrationManifest) ([]byte, error) {
	data, err := jsonv2.Marshal(manifest, jsonv2.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("encode migration manifest: %w", err)
	}
	return data, nil
}

func loadMigrationManifest(ctx context.Context, root *storeRoot, requested string, limits MaintenanceLimits) (migrationManifest, string, error) {
	path, err := validateManifestPath(root.root, requested)
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
	if err := jsonv2.Unmarshal(data, &manifest); err != nil {
		return migrationManifest{}, "", fmt.Errorf("%w: invalid migration manifest", ErrStoreCorrupt)
	}
	if manifest.Format != maintenanceManifestFormat || manifest.SourceSchemaVersion != schemaVersion ||
		manifest.TargetStoreFormat != storeFormatV2 || filepath.Clean(manifest.StoreRoot) != root.root ||
		manifest.RootIdentity != root.rootIdentity || manifest.BackupIdentity != backupIdentity || !filepath.IsAbs(manifest.CWDBase) {
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

func openBackupRoot(root *storeRoot, name string) (*os.Root, string, error) {
	if root == nil || root.rootHandle == nil || filepath.Base(name) != name || !isBackupDirectoryName(name) {
		return nil, "", fmt.Errorf("%w: backup directory name is unsafe", ErrStoreCorrupt)
	}
	directory, err := openRootDirectory(root.rootHandle, name)
	if err != nil {
		return nil, "", err
	}
	identity, identityErr := storeFileIdentity(directory)
	closeErr := directory.Close()
	if identityErr != nil || closeErr != nil || identity == "" || len(identity) > maxStoreIdentityBytes {
		return nil, "", fmt.Errorf("%w: backup directory ownership or permissions are unsafe", ErrStoreCorrupt)
	}
	backup, err := root.rootHandle.OpenRoot(name)
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
			entry.SourceIdentity == "" || len(entry.SourceIdentity) > maxStoreIdentityBytes || manifest.BackupIdentity == "" || len(manifest.BackupIdentity) > maxStoreIdentityBytes {
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
			if _, duplicate := seenNames[name]; duplicate {
				return fmt.Errorf("%w: duplicate manifest filename", ErrStoreCorrupt)
			}
			seenNames[name] = struct{}{}
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

func verifyBackup(ctx context.Context, root *storeRoot, manifest migrationManifest, manifestPath string, limits MaintenanceLimits) error {
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
