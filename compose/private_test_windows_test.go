//go:build windows

package compose_test

import (
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func privateComposeTempDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	inherit := uint32(windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
	entries := make([]windows.EXPLICIT_ACCESS, 0, 3)
	var pinner runtime.Pinner
	defer pinner.Unpin()
	for _, sid := range []*windows.SID{current.User.Sid, system, administrators} {
		pinner.Pin(sid)
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(entries)
	return path
}
