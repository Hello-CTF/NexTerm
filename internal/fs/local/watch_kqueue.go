//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package local

// kqueueEntryLimit bounds the entries of a watched directory: kqueue holds one
// descriptor per entry, so larger directories are polled instead.
const kqueueEntryLimit = 2000
