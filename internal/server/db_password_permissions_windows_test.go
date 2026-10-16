//go:build windows

package server

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestResolveDBPasswordWindowsSIDConversionFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*dbPasswordWindowsAPIFuncs)
		want   string
	}{
		{
			name: "current user conversion error",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.sidToString = func(*windows.SID) (string, error) {
					return "", errors.New("current SID conversion failure")
				}
			},
			want: "current SID conversion failure",
		},
		{
			name: "current user empty SID",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.sidToString = func(*windows.SID) (string, error) { return "", nil }
			},
			want: "empty SID",
		},
		{
			name: "ACE conversion error",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				convert := api.sidToString
				calls := 0
				api.sidToString = func(sid *windows.SID) (string, error) {
					calls++
					if calls == 1 {
						return convert(sid)
					}
					return "", errors.New("ACE SID conversion failure")
				}
			},
			want: "ACE SID conversion failure",
		},
		{
			name: "ACE empty SID",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				convert := api.sidToString
				calls := 0
				api.sidToString = func(sid *windows.SID) (string, error) {
					calls++
					if calls == 1 {
						return convert(sid)
					}
					return "", nil
				}
			},
			want: "empty SID",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passwordFile := dbPasswordWindowsTestFile(t)
			api := stubDBPasswordWindowsAPI(t)
			tc.mutate(api)
			password, err := ResolveDBPassword(passwordFile)
			requireDBPasswordWindowsError(t, password, err, tc.want)
		})
	}
}

func TestDBPasswordSIDStringRejectsInvalidSID(t *testing.T) {
	raw := make([]byte, 64)
	sid := (*windows.SID)(unsafe.Pointer(&raw[0]))
	value, err := dbPasswordSIDString(sid)
	if err == nil || value != "" {
		t.Fatalf("invalid SID conversion = %q, %v", value, err)
	}
}

func TestResolveDBPasswordWindowsDACLFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*dbPasswordWindowsAPIFuncs)
		want   string
	}{
		{
			name: "missing descriptor",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.getNamedSecurityInfo = func(string, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, error) {
					return nil, nil
				}
			},
			want: "no security descriptor",
		},
		{
			name: "security info API error",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.getNamedSecurityInfo = func(string, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, error) {
					return nil, errors.New("security API failure")
				}
			},
			want: "security API failure",
		},
		{
			name: "missing DACL",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.getDACL = func(*windows.SECURITY_DESCRIPTOR) (*windows.ACL, bool, error) {
					return nil, false, errors.New("missing DACL")
				}
			},
			want: "missing DACL",
		},
		{
			name: "null DACL",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.getDACL = func(*windows.SECURITY_DESCRIPTOR) (*windows.ACL, bool, error) {
					return nil, false, nil
				}
			},
			want: "null DACL",
		},
		{
			name: "empty DACL",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.getDACL = func(*windows.SECURITY_DESCRIPTOR) (*windows.ACL, bool, error) {
					return &windows.ACL{}, false, nil
				}
			},
			want: "empty DACL",
		},
		{
			name: "token error",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.currentUserSID = func() (*windows.SID, error) {
					return nil, errors.New("token failure")
				}
			},
			want: "token failure",
		},
		{
			name: "ACE API error",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.getAce = func(*windows.ACL, uint32, **windows.ACCESS_ALLOWED_ACE) error {
					return errors.New("ACE API failure")
				}
			},
			want: "ACE API failure",
		},
		{
			name: "deny ACE",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.getAce = func(_ *windows.ACL, _ uint32, ace **windows.ACCESS_ALLOWED_ACE) error {
					*ace = &windows.ACCESS_ALLOWED_ACE{Header: windows.ACE_HEADER{AceType: windows.ACCESS_DENIED_ACE_TYPE}}
					return nil
				}
			},
			want: "deny ACL entry",
		},
		{
			name: "unknown ACE",
			mutate: func(api *dbPasswordWindowsAPIFuncs) {
				api.getAce = func(_ *windows.ACL, _ uint32, ace **windows.ACCESS_ALLOWED_ACE) error {
					*ace = &windows.ACCESS_ALLOWED_ACE{Header: windows.ACE_HEADER{AceType: 99}}
					return nil
				}
			},
			want: "unsupported ACL entry type 99",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passwordFile := dbPasswordWindowsTestFile(t)
			api := stubDBPasswordWindowsAPI(t)
			tc.mutate(api)
			password, err := ResolveDBPassword(passwordFile)
			requireDBPasswordWindowsError(t, password, err, tc.want)
		})
	}
}

func requireDBPasswordACLForTest(t *testing.T, path string, expectedSIDs []string) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read password file security descriptor: %v", err)
	}
	if descriptor == nil {
		t.Fatal("password file has no security descriptor")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read password file DACL: %v", err)
	}
	if dacl == nil {
		t.Fatal("password file has a null DACL")
	}
	if int(dacl.AceCount) != len(expectedSIDs) {
		t.Fatalf("password file ACE count = %d, want %d", dacl.AceCount, len(expectedSIDs))
	}
	expected := make(map[string]bool, len(expectedSIDs))
	for _, sid := range expectedSIDs {
		expected[sid] = true
	}
	seen := make(map[string]bool, len(expectedSIDs))
	for index := 0; index < int(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			t.Fatalf("read password file ACL entry: %v", err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("password file ACE type = %d, want access allowed", ace.Header.AceType)
		}
		sid, err := dbPasswordSIDString((*windows.SID)(unsafe.Pointer(&ace.SidStart)))
		if err != nil {
			t.Fatalf("convert password file ACE SID: %v", err)
		}
		if !expected[sid] {
			t.Fatalf("password file ACL has unexpected SID %s", sid)
		}
		if seen[sid] {
			t.Fatalf("password file ACL has duplicate SID %s", sid)
		}
		seen[sid] = true
	}
	for _, sid := range expectedSIDs {
		if !seen[sid] {
			t.Fatalf("password file ACL is missing SID %s", sid)
		}
	}
}

func dbPasswordWindowsTestFile(t *testing.T) string {
	t.Helper()
	passwordFile := filepath.Join(t.TempDir(), "pg.password")
	writeDBPasswordFileForTest(t, passwordFile, "file-secret\n", 0o600, "")
	return passwordFile
}

var dbPasswordWindowsAPIStubMutex sync.Mutex

func stubDBPasswordWindowsAPI(t *testing.T) *dbPasswordWindowsAPIFuncs {
	t.Helper()
	dbPasswordWindowsAPIStubMutex.Lock()
	original := dbPasswordWindowsAPI
	t.Cleanup(func() {
		dbPasswordWindowsAPI = original
		dbPasswordWindowsAPIStubMutex.Unlock()
	})
	return &dbPasswordWindowsAPI
}

func requireDBPasswordWindowsError(t *testing.T, password string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("password file was accepted")
	}
	if password != "" {
		t.Fatal("failed resolution returned password content")
	}
	if strings.Contains(err.Error(), "file-secret") {
		t.Fatal("error contains password content")
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}
