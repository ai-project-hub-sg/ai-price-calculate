//go:build windows

package auth

import (
	"fmt"
	"os"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const (
	windowsCredentialTarget = "aipc-server/admin"
	credTypeGeneric         = 1
)

// CREDENTIALW mirrors the WinAPI CREDENTIALW structure.
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	advapi32      = syscall.NewLazyDLL("advapi32.dll")
	procCredReadW = advapi32.NewProc("CredReadW")
	procCredFree  = advapi32.NewProc("CredFree")
)

// LoadCredentials prefers environment variables, then reads the generic Windows credential.
func LoadCredentials() (Credentials, error) {
	username, usernameSet := os.LookupEnv(adminUsernameEnv)
	password, passwordSet := os.LookupEnv(adminPasswordEnv)
	if usernameSet && passwordSet {
		return validateCredentials(Credentials{Username: username, Password: password})
	}
	if usernameSet || passwordSet {
		return Credentials{}, fmt.Errorf("%s and %s must both be set", adminUsernameEnv, adminPasswordEnv)
	}
	return readWindowsCredentials()
}

func readWindowsCredentials() (Credentials, error) {
	target, err := syscall.UTF16PtrFromString(windowsCredentialTarget)
	if err != nil {
		return Credentials{}, fmt.Errorf("encode Windows credential target: %w", err)
	}

	var credential *credentialW
	result, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&credential)),
	)
	if result == 0 {
		return Credentials{}, fmt.Errorf("read Windows credential %q: %w", windowsCredentialTarget, callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(credential)))

	if credential == nil || credential.UserName == nil {
		return Credentials{}, fmt.Errorf("Windows credential %q has no username", windowsCredentialTarget)
	}
	if credential.CredentialBlobSize == 0 || credential.CredentialBlob == nil {
		return Credentials{}, fmt.Errorf("Windows credential %q has no password", windowsCredentialTarget)
	}
	if credential.CredentialBlobSize%2 != 0 {
		return Credentials{}, fmt.Errorf("Windows credential %q has an invalid password encoding", windowsCredentialTarget)
	}

	blob := unsafe.Slice(credential.CredentialBlob, int(credential.CredentialBlobSize))
	passwordUnits := make([]uint16, len(blob)/2)
	for i := range passwordUnits {
		passwordUnits[i] = uint16(blob[2*i]) | uint16(blob[2*i+1])<<8
	}
	return validateCredentials(Credentials{
		Username: utf16PointerString(credential.UserName),
		Password: string(utf16.Decode(passwordUnits)),
	})
}

func utf16PointerString(value *uint16) string {
	if value == nil {
		return ""
	}
	length := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(value), uintptr(length)*unsafe.Sizeof(uint16(0)))) != 0 {
		length++
	}
	return string(utf16.Decode(unsafe.Slice(value, length)))
}
