//go:build !windows

package browserlogin

import "errors"

func acquireLoginLock() (func(), error) {
	return nil, errors.New("dedicated Edge sign-in is currently supported on Windows")
}
