//go:build windows

package securefs

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestOwnedByServiceIdentityAcceptsTokenDefaultOwner(t *testing.T) {
	descriptor, err := windows.GetNamedSecurityInfo(t.TempDir(), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		t.Fatal(err)
	}
	user, tokenOwner, _, _, err := privateSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !ownedByServiceIdentity(owner, user, tokenOwner) {
		t.Fatalf("temp dir owner %v is not the service identity (user=%v tokenOwner=%v)", owner, user, tokenOwner)
	}
}

func TestOwnedByServiceIdentityRejectsUnrelatedSID(t *testing.T) {
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	user, tokenOwner, _, _, err := privateSIDs()
	if err != nil {
		t.Fatal(err)
	}
	if ownedByServiceIdentity(world, user, tokenOwner) {
		t.Fatal("Everyone SID was accepted as the service identity")
	}
	if ownedByServiceIdentity(nil, user, tokenOwner) {
		t.Fatal("nil owner was accepted as the service identity")
	}
}
