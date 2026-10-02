// Package ssh implements the shared pure-Go SSH transport.
//
// The default tests use in-process SSH/SFTP servers. Real OpenSSH interoperability
// is opt-in with NEXTERM_SSH_INTEROP=1 and these settings:
//
//   - NEXTERM_SSH_TEST_HOST: host:port of a Unix-like OpenSSH server with tar.
//   - NEXTERM_SSH_TEST_USER: login user.
//   - NEXTERM_SSH_TEST_HOST_KEY: expected SHA256 host-key fingerprint.
//   - NEXTERM_SSH_TEST_PASSWORD or NEXTERM_SSH_TEST_KEY: password or private-key path.
//   - NEXTERM_SSH_TEST_PASSPHRASE: optional private-key passphrase.
//
// The interoperability test requires fingerprint-pinned trust and creates data only
// in a unique /tmp directory, which it removes afterward. Missing opt-in settings,
// unavailable Unix sockets on Windows, and other platform-specific cases are reported
// as explicit test skips rather than silent passes.
//
// The transport and SFTP packages require no cgo. Cross-build verification commands:
//
//	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./internal/transport/... ./internal/fs/...
//	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./internal/transport/... ./internal/fs/...
//	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./internal/transport/... ./internal/fs/...
package ssh
