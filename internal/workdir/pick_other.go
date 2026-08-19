//go:build !windows && !darwin && !linux

package workdir

import "context"

func pickNative(context.Context, string) (string, error) {
	return "", ErrUnavailable
}
