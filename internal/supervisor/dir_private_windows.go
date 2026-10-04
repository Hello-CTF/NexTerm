//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is not a real directory", ErrInvalidInput, path)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidInput, path, err)
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return fmt.Errorf("inspect directory attributes: %w", err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: %s is a reparse point", ErrInvalidInput, path)
	}
	return makeDirPrivate(path)
}

func makeDirPrivate(path string) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect directory owner: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("inspect directory owner: %w", err)
	}
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return fmt.Errorf("current process token: %w", err)
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("current process token: %w", err)
	}
	if !windows.EqualSid(owner, user.User.Sid) {
		return fmt.Errorf("%w: %s is owned by another user", ErrInvalidInput, path)
	}
	sid, err := currentUserSIDString()
	if err != nil {
		return fmt.Errorf("current user SID: %w", err)
	}
	privateDescriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;" + sid + ")")
	if err != nil {
		return fmt.Errorf("build private directory security descriptor: %w", err)
	}
	dacl, _, err := privateDescriptor.DACL()
	if err != nil {
		return fmt.Errorf("build private directory DACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("protect directory DACL: %w", err)
	}
	return nil
}

func privateRegularFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: not a regular file", ErrInvalidInput)
	}
	return nil
}
