//go:build !windows

package sessions

import (
	"os"
	"time"
)

func renameReplace(root *os.Root, oldName, newName string) error {
	const attempts = 20
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = root.Rename(oldName, newName)
		if err == nil {
			return nil
		}
		if !isTransientRenameError(err) || attempt+1 == attempts {
			return err
		}
		time.Sleep(time.Duration(5*(attempt+1)) * time.Millisecond)
	}
	return err
}

// isTransientRenameError is always false outside Windows: POSIX rename is
// uncontended against open readers, so a failed rename is permanent.
func isTransientRenameError(err error) bool {
	return false
}
