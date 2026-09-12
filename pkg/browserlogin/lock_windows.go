//go:build windows

package browserlogin

import (
	"errors"
	"syscall"
	"unsafe"
)

func acquireLoginLock() (func(), error) {
	kernel := syscall.NewLazyDLL("kernel32.dll")
	name, _ := syscall.UTF16PtrFromString(`Local\M365BridgeBrowserSignIn`)
	handle, _, lastError := kernel.NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return nil, errors.New("Could not reserve the browser sign-in window.")
	}
	close := func() { _, _, _ = kernel.NewProc("CloseHandle").Call(handle) }
	if lastError == syscall.ERROR_ALREADY_EXISTS {
		close()
		return nil, errors.New("A Microsoft sign-in window is already open.")
	}
	return close, nil
}
