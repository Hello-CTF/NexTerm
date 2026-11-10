package winrm

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestCreateFileUsesCreateNewAndReportsAnExistingPath(t *testing.T) {
	executor := &fakeExecutor{result: success("created\r\n")}
	filesystem := New(executor)
	if err := filesystem.CreateFile(context.Background(), `C:\srv\O'Brien.conf`); err != nil {
		t.Fatal(err)
	}
	script := executor.scripts[0]
	for _, fragment := range []string{
		`GetUnresolvedProviderPathFromPSPath('C:\srv\O''Brien.conf'`,
		`[IO.FileMode]::CreateNew`,
	} {
		if !strings.Contains(script, fragment) {
			t.Errorf("create script missing %q: %s", fragment, script)
		}
	}
	if strings.Contains(script, "WriteAllBytes") {
		t.Fatalf("create script can truncate an existing file: %s", script)
	}

	executor.result = success("exists")
	if err := filesystem.CreateFile(context.Background(), `C:\srv\nginx.conf`); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateFile over an existing path = %v, want fs.ErrExist", err)
	}
	executor.result = success("")
	if err := filesystem.CreateFile(context.Background(), `C:\srv\nginx.conf`); err == nil {
		t.Fatal("CreateFile accepted an empty response")
	}
}
