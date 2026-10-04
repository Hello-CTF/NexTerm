//go:build unix

package platform

import "syscall"

const DefaultNoFileLimit = 1048576

func RaiseNoFileLimit(target uint64) (uint64, error) {
	if target == 0 {
		target = DefaultNoFileLimit
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return 0, err
	}
	desired := target
	if limit.Max < desired {
		desired = limit.Max
	}
	if desired <= limit.Cur {
		return limit.Cur, nil
	}
	limit.Cur = desired
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return limit.Cur, err
	}
	return desired, nil
}
