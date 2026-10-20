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

export function parseReleaseVersion(value) {
  const parsed = parseSemVer(value);
  return parsed && value !== "0.0.0" ? parsed : null;
}
