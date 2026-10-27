package supervisor

// lockHelperState takes the lock a running helper holds on its state
// directory; helpers of every protocol use the same file.
func lockHelperState(stateDir string) (func(), error) {
	return lockStateFile(stateDir, "helper.lock")
}

// lockHelperReplace takes the lock that serializes replacements of an older
// helper for one state directory, across processes.
func lockHelperReplace(stateDir string) (func(), error) {
	return lockStateFile(stateDir, "helper.replace.lock")
}
