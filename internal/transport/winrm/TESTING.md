# WinRM integration fixtures

Unit tests run by default. They include in-process HTTP/SOAP fixtures for Basic/domain credentials, command execution, explicit HTTP proxying, and TLS verification modes; these fixtures are not claims of real Windows Server or NTLM handshake validation. Real Windows Server tests are opt-in and never run against an arbitrary host:

```sh
NEXTERM_WINRM_INTEGRATION=1 \
NEXTERM_WINRM_HOST=windows.example \
NEXTERM_WINRM_USER=Administrator \
NEXTERM_WINRM_PASSWORD='...' \
go test -v ./internal/transport/winrm -run TestRealWindowsServer
```

Optional settings are `NEXTERM_WINRM_PORT`, `NEXTERM_WINRM_DOMAIN`, `NEXTERM_WINRM_AUTH=ntlm|basic`, `NEXTERM_WINRM_TLS=true`, `NEXTERM_WINRM_ACCEPT_INVALID_CERTS=true`, `NEXTERM_WINRM_TLS_SERVER_NAME`, `NEXTERM_WINRM_CA_FILE`, `NEXTERM_WINRM_PROXY_URL`, and `NEXTERM_WINRM_INITIAL_CWD`. The default is NTLM on port 5985 with certificate verification enabled for TLS. Port 5986 implies TLS.

Fixture coverage and explicit unavailable cases:

- Authentication runs against the configured account. Run the suite once with `NEXTERM_WINRM_AUTH=ntlm` and again with `basic` to certify both; neither is claimed from a run of the other mode.
- The domain-user subtest skips unless `NEXTERM_WINRM_DOMAIN` is set or the user is already qualified as `DOMAIN\\user` or `user@domain`. A local Administrator account is not a domain fixture.
- The TLS subtest skips unless the fixture enables `NEXTERM_WINRM_TLS` or uses port 5986. A trusted CA can be supplied with `NEXTERM_WINRM_CA_FILE`; skipping verification is supported but is not a certificate-validation test.
- The proxy subtest skips unless `NEXTERM_WINRM_PROXY_URL` is set. The fixture proxy must support the selected HTTP/HTTPS/SOCKS5 route; unit tests separately prove that environment proxies are ignored.
- cwd persistence, nonzero exit status, UTF-8, GB18030, separate stdout/stderr, and filesystem operations run on the main fixture. Filesystem tests create and recursively remove a unique apostrophe-containing directory under the remote temporary directory.
- Kerberos, interactive PTY, streaming file transfer, and streaming Docker exec have no fixture and are not implemented or claimed.

In this development environment no real Windows Server, domain controller, WinRM TLS listener, or explicit WinRM proxy fixture has been supplied. Consequently those tests must report Skip here; their successful execution remains outstanding until the corresponding environment is provided.
