//go:build windows

package server

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type dbPasswordWindowsAPIFuncs struct {
	getNamedSecurityInfo func(string, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, error)
	getDACL              func(*windows.SECURITY_DESCRIPTOR) (*windows.ACL, bool, error)
	currentUserSID       func() (*windows.SID, error)
	getAce               func(*windows.ACL, uint32, **windows.ACCESS_ALLOWED_ACE) error
	sidToString          func(*windows.SID) (string, error)
}

func defaultDBPasswordWindowsAPI() dbPasswordWindowsAPIFuncs {
	return dbPasswordWindowsAPIFuncs{
		getNamedSecurityInfo: windows.GetNamedSecurityInfo,
		getDACL: func(descriptor *windows.SECURITY_DESCRIPTOR) (*windows.ACL, bool, error) {
			return descriptor.DACL()
		},
		currentUserSID: currentDBPasswordUserSID,
		getAce:         windows.GetAce,
		sidToString:    dbPasswordSIDString,
	}
}

func validateDBPasswordFilePermissions(passwordFile string, _ os.FileInfo) error {
	return validateDBPasswordFilePermissionsWithAPI(passwordFile, defaultDBPasswordWindowsAPI())
}

func validateDBPasswordFilePermissionsWithAPI(passwordFile string, api dbPasswordWindowsAPIFuncs) error {
	descriptor, err := api.getNamedSecurityInfo(passwordFile, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("inspect database password file %s ACL: %w", passwordFile, err)
	}
	if descriptor == nil {
		return fmt.Errorf("database password file %s has no security descriptor", passwordFile)
	}
	dacl, _, err := api.getDACL(descriptor)
	if err != nil {
		return fmt.Errorf("inspect database password file %s ACL: %w", passwordFile, err)
	}
	if dacl == nil {
		return fmt.Errorf("database password file %s has a null DACL", passwordFile)
	}
	if dacl.AceCount == 0 {
		return fmt.Errorf("database password file %s has an empty DACL", passwordFile)
	}
	currentSID, err := api.currentUserSID()
	if err != nil {
		return fmt.Errorf("inspect current user for database password file %s ACL: %w", passwordFile, err)
	}
	currentUserSID, err := api.sidToString(currentSID)
	if err != nil {
		return fmt.Errorf("convert current user SID for database password file %s ACL: %w", passwordFile, err)
	}
	if currentUserSID == "" {
		return fmt.Errorf("convert current user SID for database password file %s ACL: empty SID", passwordFile)
	}
	for index := 0; index < int(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := api.getAce(dacl, uint32(index), &ace); err != nil {
			return fmt.Errorf("inspect database password file %s ACL entry: %w", passwordFile, err)
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			return fmt.Errorf("database password file %s contains a deny ACL entry", passwordFile)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("database password file %s has unsupported ACL entry type %d", passwordFile, ace.Header.AceType)
		}
		sid, err := api.sidToString((*windows.SID)(unsafe.Pointer(&ace.SidStart)))
		if err != nil {
			return fmt.Errorf("convert SID in database password file %s ACL entry %d: %w", passwordFile, index, err)
		}
		if sid == "" {
			return fmt.Errorf("convert SID in database password file %s ACL entry %d: empty SID", passwordFile, index)
		}
		switch sid {
		case currentUserSID, "S-1-5-18", "S-1-5-32-544":
		default:
			return fmt.Errorf("database password file %s grants access to disallowed SID %s", passwordFile, sid)
		}
	}
	return nil
}

func currentDBPasswordUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func dbPasswordSIDString(sid *windows.SID) (string, error) {
	var encoded *uint16
	if err := windows.ConvertSidToStringSid(sid, &encoded); err != nil {
		return "", err
	}
	if encoded == nil {
		return "", errors.New("SID conversion returned an empty string")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(encoded)))
	value := windows.UTF16PtrToString(encoded)
	if value == "" {
		return "", errors.New("SID conversion returned an empty string")
	}
	return value, nil
}
