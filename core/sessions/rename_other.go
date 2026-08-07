//go:build !windows

package sessions

// isTransientRenameError is always false outside Windows: POSIX rename is
// uncontended against open readers, so a failed rename is permanent.
func isTransientRenameError(err error) bool {
	return false
}
