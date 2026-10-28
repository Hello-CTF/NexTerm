// One-click install command construction for fresh machines (FLEET168): curl fetches
// /install-device.sh (M157) from the selected base URL and runs it with a one-time enroll code and the real server version.
// Contract aligned with the usage line of public/install-device.sh: --server/--code/--version are required,
// --insecure follows the base URL config. The version comes from /healthz, guaranteed non-empty by the caller - when the version
// is unavailable the whole UI section must hide; never fabricate a version and never claim install success.

// shellQuote wraps dynamic arguments (base URL/enroll code/version) in single-quoted safe form, preventing
// characters like ; & $() in URL paths from being treated as shell command syntax (the server-side normalizeBaseURL allows
// these characters).
export function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

// insecure relaxes the certificate check only when explicitly configured for that base URL: the curl downloading the script also gets
// --insecure (with self-signed HTTPS the left side of the pipe would otherwise fail the certificate first and the script never receives content),
// and --insecure is kept for the script/enroll; addresses without insecure configured are strictly verified on both sides.
export function oneLineInstallCommand(baseUrl: string, insecure: boolean, code: string, version: string): string {
  const scriptUrl = `${baseUrl}/install-device.sh`;
  return `curl -fsSL${insecure ? " --insecure" : ""} ${shellQuote(scriptUrl)} | sh -s -- --server ${shellQuote(baseUrl)} --code ${shellQuote(code)} --version ${shellQuote(version)}${insecure ? " --insecure" : ""}`;
}

// psQuote is PowerShell literal string quoting: wrapped in single quotes, embedded single quotes escaped by doubling
// (the '\'' form of shellQuote is not valid in PowerShell).
export function psQuote(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}

// Windows devices go through the PowerShell install-device.ps1: only the iex-wrapped-scriptblock form
// can pass --server/--code/--version through to the remote script (same parameter names as the sh version); a direct
// iwr ... | iex pipe cannot take arguments. The ps1 is provided as a separate deliverable; only the URL and argument shape are agreed here.
export function oneLineInstallCommandWindows(baseUrl: string, insecure: boolean, code: string, version: string): string {
  const scriptUrl = `${baseUrl}/install-device.ps1`;
  return `iex "& { $(iwr -UseBasicParsing ${psQuote(scriptUrl)}) } -Server ${psQuote(baseUrl)} -Code ${psQuote(code)} -Version ${psQuote(version)}${insecure ? " -Insecure" : ""}"`;
}

// releaseOrNull keeps only versions that can exist as a GitHub release (x.y.z with an optional pre-release suffix).
// Development builds report "dev" or an empty string; install-device downloads v<version> assets, so these map to null.
export function releaseOrNull(version: string | null | undefined): string | null {
  const v = (version ?? "").trim();
  return /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(v) && !/^0\.0\.0(?![\d.])/.test(v) ? v : null;
}
