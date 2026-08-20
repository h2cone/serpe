package sessions

import (
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	storeFormatName    = ".serpe-store-format"
	storeLockName      = ".serpe-store.lock"
	storeFormatV2      = "serpe.sessions.filestore.v2\n"
	formatTempPrefix   = storeFormatName + "."
	recordTempSuffix   = ".tmp"
	recordNamePrefixV2 = "r2_"
	recordNameSuffixV2 = ".json"
)

var recordBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func encodeRecordName(id string) string {
	encoded := strings.ToLower(recordBase32.EncodeToString([]byte(id)))
	return recordNamePrefixV2 + encoded + recordNameSuffixV2
}

func decodeRecordName(name string) (string, error) {
	if !strings.HasPrefix(name, recordNamePrefixV2) || !strings.HasSuffix(name, recordNameSuffixV2) {
		return "", fmt.Errorf("invalid record name")
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, recordNamePrefixV2), recordNameSuffixV2)
	if body == "" || body != strings.ToLower(body) || strings.Contains(body, "=") {
		return "", fmt.Errorf("invalid record base32")
	}
	decoded, err := recordBase32.DecodeString(strings.ToUpper(body))
	if err != nil {
		return "", err
	}
	id := string(decoded)
	if !validID(id) || encodeRecordName(id) != name {
		return "", fmt.Errorf("non-canonical record name")
	}
	return id, nil
}

func isJSONCandidate(name string) bool {
	return len(name) >= len(recordNameSuffixV2) && strings.EqualFold(name[len(name)-len(recordNameSuffixV2):], recordNameSuffixV2)
}

func isOwnedTempName(name string) bool {
	if len(name) > 250 || !strings.HasSuffix(name, recordTempSuffix) {
		return false
	}
	withoutSuffix := strings.TrimSuffix(name, recordTempSuffix)
	base, hexadecimal, found := strings.CutLast(withoutSuffix, ".")
	if !found {
		return false
	}
	if len(hexadecimal) != 32 || hexadecimal != strings.ToLower(hexadecimal) {
		return false
	}
	if _, err := hex.DecodeString(hexadecimal); err != nil {
		return false
	}
	if base == storeFormatName {
		return true
	}
	_, err := decodeRecordName(base)
	return err == nil
}

func (r *storeRoot) initializeLayout() error {
	markerInfo, err := r.rootHandle.Lstat(storeFormatName)
	if err == nil {
		if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
			return fmt.Errorf("%w: invalid format marker", ErrStoreCorrupt)
		}
		marker, err := openRootRegular(r.rootHandle, storeFormatName, os.O_RDONLY)
		if err != nil {
			return fmt.Errorf("%w: open format marker", ErrStoreCorrupt)
		}
		info, statErr := marker.Stat()
		if statErr != nil || validateStoreRegular(marker, info) != nil {
			_ = marker.Close()
			return fmt.Errorf("%w: unsafe format marker", ErrStoreCorrupt)
		}
		data, readErr := io.ReadAll(io.LimitReader(marker, int64(len(storeFormatV2))+1))
		closeErr := marker.Close()
		if readErr != nil || closeErr != nil || string(data) != storeFormatV2 {
			return fmt.Errorf("%w: unknown format marker", ErrStoreCorrupt)
		}
		return r.scanCanonicalNames()
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("%w: inspect format marker", ErrStoreCorrupt)
	}
	entries, err := readRootDirEntries(r.rootHandle, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if isJSONCandidate(entry.Name()) {
			return fmt.Errorf("%w: legacy or unmarked JSON records found", ErrMigrationRequired)
		}
	}
	return r.publishMarker()
}

func (r *storeRoot) scanCanonicalNames() error {
	entries, err := readRootDirEntries(r.rootHandle, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !isJSONCandidate(name) {
			continue
		}
		if entry.IsDir() {
			return fmt.Errorf("%w: JSON candidate is a directory", ErrStoreCorrupt)
		}
		if _, err := decodeRecordName(name); err != nil {
			return fmt.Errorf("%w: non-canonical record filename", ErrStoreCorrupt)
		}
		listed, err := r.rootHandle.Lstat(name)
		if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() {
			return fmt.Errorf("%w: unsafe record entry", ErrStoreCorrupt)
		}
		file, err := openRootRegular(r.rootHandle, name, os.O_RDONLY)
		if err != nil {
			return fmt.Errorf("%w: open record entry", ErrStoreCorrupt)
		}
		info, statErr := file.Stat()
		validationErr := validateStoreRegular(file, info)
		closeErr := file.Close()
		if statErr != nil || validationErr != nil || closeErr != nil {
			return fmt.Errorf("%w: unsafe record entry", ErrStoreCorrupt)
		}
	}
	return nil
}

func (r *storeRoot) publishMarker() error {
	return r.publishNamed(nil, storeFormatName, []byte(storeFormatV2),
		fmt.Errorf("%w: format marker appeared unexpectedly", ErrStoreCorrupt))
}

func (r *storeRoot) cleanupOwnedTemps() {
	entries, err := readRootDirEntries(r.rootHandle, ".")
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !isOwnedTempName(entry.Name()) {
			continue
		}
		listed, err := r.rootHandle.Lstat(entry.Name())
		if err != nil || listed.Mode()&os.ModeSymlink != 0 || !listed.Mode().IsRegular() {
			continue
		}
		file, err := openRootRegular(r.rootHandle, entry.Name(), os.O_RDONLY)
		if err != nil {
			continue
		}
		info, statErr := file.Stat()
		validationErr := validateStoreRegular(file, info)
		closeErr := file.Close()
		if statErr != nil || validationErr != nil || closeErr != nil {
			continue
		}
		_ = r.rootHandle.Remove(entry.Name())
	}
}
