package base

// PathSeparators returns the characters that separate components of a remote path. A path rooted at "/" or "~/"
// is POSIX, where a backslash is an ordinary filename character; anything else (a drive letter, a UNC share or a
// relative name) also accepts the Windows backslash.
func PathSeparators(path string) string {
	if path == "~" || (len(path) > 0 && path[0] == '/') || (len(path) > 1 && path[0] == '~' && path[1] == '/') {
		return "/"
	}
	return `/\`
}
