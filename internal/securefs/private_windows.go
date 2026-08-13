//go:build windows

// Package securefs centralizes private-file ownership and access checks used
// by persistent stores and server secrets.
package securefs

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const accessAllowedCompoundACEType = 4

var accessGrantingACETypes = map[uint8]struct{}{
	windows.ACCESS_ALLOWED_ACE_TYPE: {},
	accessAllowedCompoundACEType:    {},
	5:                               {}, // ACCESS_ALLOWED_OBJECT_ACE_TYPE
	9:                               {}, // ACCESS_ALLOWED_CALLBACK_ACE_TYPE
	11:                              {}, // ACCESS_ALLOWED_CALLBACK_OBJECT_ACE_TYPE
}

// ValidatePrivate requires ownership by the current service token and permits
// access-granting ACEs only for that token, LocalSystem, and Administrators.
// The caller remains responsible for the expected file type.
//
// On Windows the token user and the token default owner can differ: an
// elevated administrator creates objects owned by Administrators. Either SID
// is the current service identity for ownership checks.
func ValidatePrivate(file *os.File, info os.FileInfo) error {
	if file == nil || info == nil {
		return fmt.Errorf("securefs: missing file handle or metadata")
	}
	var handleInfo windows.ByHandleFileInformation
	handle := windows.Handle(file.Fd())
	if err := windows.GetFileInformationByHandle(handle, &handleInfo); err != nil {
		return fmt.Errorf("securefs: inspect file handle: %w", err)
	}
	if handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("securefs: reparse points are not permitted")
	}

	user, tokenOwner, system, administrators, err := privateSIDs()
	if err != nil {
		return err
	}
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("securefs: read security descriptor: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || !ownedByServiceIdentity(owner, user, tokenOwner) {
		return fmt.Errorf("securefs: file is not owned by the current service identity")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("securefs: a bounded DACL is required")
	}
	allowed := []*windows.SID{user, system, administrators}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil {
			return fmt.Errorf("securefs: read DACL entry %d", index)
		}
		if _, grantsAccess := accessGrantingACETypes[ace.Header.AceType]; !grantsAccess {
			continue
		}
		// Object, callback-object, and compound allow ACEs have a different
		// variable layout. Rejecting them is conservative and keeps the policy
		// auditable instead of guessing where their trustee SID begins.
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("securefs: unsupported access-granting ACE type %d", ace.Header.AceType)
		}
		if ace.Header.AceSize < uint16(unsafe.Offsetof(ace.SidStart))+8 {
			return fmt.Errorf("securefs: malformed access-granting ACE")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		sidLength := sid.Len()
		if !sid.IsValid() || sidLength <= 0 ||
			uint64(unsafe.Offsetof(ace.SidStart))+uint64(sidLength) > uint64(ace.Header.AceSize) {
			return fmt.Errorf("securefs: malformed access-granting SID")
		}
		trusted := false
		for _, candidate := range allowed {
			if sid.Equals(candidate) {
				trusted = true
				break
			}
		}
		if !trusted && ace.Mask != 0 {
			return fmt.Errorf("securefs: DACL grants access to an untrusted identity")
		}
	}
	return nil
}

func ownedByServiceIdentity(owner, user, tokenOwner *windows.SID) bool {
	if owner == nil || user == nil {
		return false
	}
	if owner.Equals(user) {
		return true
	}
	return tokenOwner != nil && owner.Equals(tokenOwner)
}

func privateSIDs() (user, tokenOwner, system, administrators *windows.SID, err error) {
	token := windows.GetCurrentProcessToken()
	current, err := token.GetTokenUser()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("securefs: read service identity: %w", err)
	}
	user, err = current.User.Sid.Copy()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("securefs: copy service identity: %w", err)
	}
	tokenOwner, err = tokenDefaultOwner(token)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	system, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("securefs: create LocalSystem SID: %w", err)
	}
	administrators, err = windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("securefs: create Administrators SID: %w", err)
	}
	return user, tokenOwner, system, administrators, nil
}

func tokenDefaultOwner(token windows.Token) (*windows.SID, error) {
	needed := uint32(50)
	for {
		buf := make([]byte, needed)
		err := windows.GetTokenInformation(token, windows.TokenOwner, &buf[0], uint32(len(buf)), &needed)
		if err == windows.ERROR_INSUFFICIENT_BUFFER {
			if needed <= uint32(len(buf)) {
				return nil, fmt.Errorf("securefs: read token owner: %w", err)
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("securefs: read token owner: %w", err)
		}
		sid := *(**windows.SID)(unsafe.Pointer(&buf[0]))
		if sid == nil || !sid.IsValid() {
			return nil, fmt.Errorf("securefs: invalid token owner")
		}
		copied, err := sid.Copy()
		if err != nil {
			return nil, fmt.Errorf("securefs: copy token owner: %w", err)
		}
		return copied, nil
	}
}
