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

function desktopArtifact({ os, arch }, version) {
  if (os === "windows") {
    const packageArch = { amd64: "x64", arm64: "arm64" }[arch];
    if (!packageArch) throw new Error(`unsupported Windows desktop architecture: ${arch}`);
    return [`NexTerm_${version}_${packageArch}-setup.exe`, `desktop-nsis-windows-${arch}`];
  }
  if (os === "darwin") {
    const packageArch = { amd64: "x86_64", arm64: "aarch64" }[arch];
    if (!packageArch) throw new Error(`unsupported macOS desktop architecture: ${arch}`);
    return [`NexTerm_${version}_${packageArch}.dmg`, `desktop-dmg-darwin-${arch}`];
  }
  if (os === "linux") return [`NexTerm-desktop_${version}_linux_${arch}.deb`, `desktop-linux-deb-linux-${arch}`];
  throw new Error(`unsupported native desktop OS: ${os}`);
}

export function releaseArtifacts(version) {
  const artifacts = desktopNativeMatrix().map((entry) => desktopArtifact(entry, version));
  artifacts.push(
    [`NexTerm_${version}_android.apk`, "mobile-android"],
    [`NexTerm-agent_${version}_windows_amd64.zip`, "agent-zip-windows-amd64"],
  );
  for (const arch of ["amd64", "arm64"]) {
    artifacts.push(
      [`NexTerm-server_${version}_linux_${arch}.tar.gz`, `server-archive-linux-${arch}`],
      [`NexTerm-agent_${version}_linux_${arch}.tar.gz`, `agent-archive-linux-${arch}`],
      [`NexTerm-agent_${version}_darwin_${arch}.tar.gz`, `agent-archive-darwin-${arch}`],
    );
  }
  return new Map(artifacts);
}
