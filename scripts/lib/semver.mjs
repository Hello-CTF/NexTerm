const SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$/;

export function parseSemVer(value) {
  if (typeof value !== "string") return null;
  const match = SEMVER.exec(value);
  if (!match) return null;
  const [, major, minor, patch, prerelease, build] = match;
  return {
    version: value,
    core: `${major}.${minor}.${patch}`,
    major,
    minor,
    patch,
    prerelease: prerelease?.split(".") ?? [],
    build: build?.split(".") ?? [],
  };
}

// Release versions are x.y.z or x.y.z-rc.N (N a positive integer without leading zeros). Other prerelease forms
// (beta, alpha, rc without a number) are not published: the updater ignores them on every channel.
export function releaseVersionProblem(value) {
  const parsed = parseSemVer(value);
  if (!parsed || value === "0.0.0") return "expected a non-0.0.0 semantic version without a leading v";
  if (parsed.prerelease.length > 0) {
    const [label, number, ...rest] = parsed.prerelease;
    if (label !== "rc" || rest.length > 0 || !/^[1-9]\d*$/.test(number ?? "")) {
      return "only x.y.z or x.y.z-rc.N (N >= 1) release versions are supported";
    }
  }
  return null;
}

export function parseReleaseVersion(value) {
  return releaseVersionProblem(value) === null ? parseSemVer(value) : null;
}

// Android versionCode = major*100000000 + minor*100000 + patch*100 + (rc.N ? N : 99), so rc.1 < rc.2 < ... < final
// of one core version, every later core version is larger, and all values exceed the codes the earlier
// major*1000000 + minor*1000 + patch scheme issued (latest v1.3.6 = 1003006). Limits keep the result within the
// int32 range Google Play accepts (2100000000).
export const ANDROID_VERSION_CODE_MAX = 2100000000;

export function androidVersionCode(parsed) {
  const major = Number(parsed.major);
  const minor = Number(parsed.minor);
  const patch = Number(parsed.patch);
  if (major > 20) throw new Error("Android versionCode requires a major version no greater than 20");
  if (minor > 999 || patch > 999) throw new Error("Android versionCode requires minor and patch versions no greater than 999");
  let suffix = 99;
  if (parsed.prerelease.length > 0) {
    suffix = Number(parsed.prerelease[1]);
    if (!Number.isInteger(suffix) || suffix < 1 || suffix > 98) {
      throw new Error("Android versionCode requires a release candidate number between 1 and 98");
    }
  }
  const versionCode = major * 100000000 + minor * 100000 + patch * 100 + suffix;
  if (versionCode < 1 || versionCode > ANDROID_VERSION_CODE_MAX) {
    throw new Error(`Android versionCode ${versionCode} is outside the supported range 1..${ANDROID_VERSION_CODE_MAX}`);
  }
  return versionCode;
}
