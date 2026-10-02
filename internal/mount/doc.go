// Package mount owns operating-system network mounts and their audit records.
// Windows and SSHFS passwords are supplied only through child stdin, never argv.
// macOS mount scans remain available while create/remove fail as unsupported.
//
// Set NEXTERM_MOUNT_INTEROP=1 with NEXTERM_MOUNT_TEST_REMOTE and
// NEXTERM_MOUNT_TEST_POINT to run the destructive-by-design, opt-in mount test.
// Optional NEXTERM_MOUNT_TEST_USERNAME and NEXTERM_MOUNT_TEST_PASSWORD supply
// credentials. The test only removes a mount that it successfully created.
package mount
