//go:build unix

package store

import (
	"os"
	"syscall"
)

// matchOwner gives path the owner and group of reference when they differ.
func matchOwner(path, reference string) error {
	want, err := os.Stat(reference)
	if err != nil {
		return err
	}
	have, err := os.Lstat(path)
	if err != nil {
		return err
	}
	wantStat, ok := want.Sys().(*syscall.Stat_t)
	haveStat, ok2 := have.Sys().(*syscall.Stat_t)
	if !ok || !ok2 || (wantStat.Uid == haveStat.Uid && wantStat.Gid == haveStat.Gid) {
		return nil
	}
	return os.Lchown(path, int(wantStat.Uid), int(wantStat.Gid))
}
