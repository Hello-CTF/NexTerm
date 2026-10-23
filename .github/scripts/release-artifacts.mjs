import fs from "node:fs";

const nativeMatrix = JSON.parse(fs.readFileSync(new URL("../native-matrix.json", import.meta.url), "utf8"));

export function desktopNativeMatrix() {
  const seen = new Set();
  const entries = [];
  for (const { runner, os, arch, kind } of nativeMatrix) {
    if (kind !== "desktop") continue;
    const key = `${os}/${arch}`;
    if (seen.has(key)) throw new Error(`duplicate native desktop matrix entry: ${key}`);
    if (!runner || !os || !arch) throw new Error("native desktop matrix entries require runner, os and arch");
    seen.add(key);
    entries.push({ runner, os, arch });
  }
  return entries;
}

const publicOS = { windows: "windows", darwin: "macos", linux: "linux", android: "android" };

// releaseAssetName is the single naming rule for published files:
// nexterm-<kind>_<os>_<arch>_<version>.<ext>, with macOS spelled "macos".
export function releaseAssetName(kind, os, arch, version, ext) {
  const name = publicOS[os];
  if (!name) throw new Error(`unsupported release OS: ${os}`);
  if (arch !== "amd64" && arch !== "arm64") throw new Error(`unsupported release architecture: ${arch}`);
  return `nexterm-${kind}_${name}_${arch}_${version}.${ext}`;
}

function desktopArtifact({ os, arch }, version) {
  if (os === "windows") return [releaseAssetName("desktop", os, arch, version, "exe"), `desktop-nsis-windows-${arch}`];
  if (os === "darwin") return [releaseAssetName("desktop", os, arch, version, "dmg"), `desktop-dmg-darwin-${arch}`];
  if (os === "linux") return [releaseAssetName("desktop", os, arch, version, "deb"), `desktop-linux-deb-linux-${arch}`];
  throw new Error(`unsupported native desktop OS: ${os}`);
}

export function releaseArtifacts(version) {
  const artifacts = desktopNativeMatrix().map((entry) => desktopArtifact(entry, version));
  artifacts.push(
    [releaseAssetName("desktop", "android", "arm64", version, "apk"), "mobile-android"],
    [releaseAssetName("agent", "windows", "amd64", version, "zip"), "agent-zip-windows-amd64"],
  );
  for (const arch of ["amd64", "arm64"]) {
    artifacts.push(
      [releaseAssetName("server", "linux", arch, version, "tar.gz"), `server-archive-linux-${arch}`],
      [releaseAssetName("agent", "linux", arch, version, "tar.gz"), `agent-archive-linux-${arch}`],
      [releaseAssetName("agent", "darwin", arch, version, "tar.gz"), `agent-archive-darwin-${arch}`],
    );
  }
  return new Map(artifacts);
}
