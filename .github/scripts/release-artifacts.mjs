export function releaseArtifacts(version) {
  return new Map([
    [`NexTerm_${version}_x64-setup.exe`, "desktop-nsis-windows-amd64"],
    [`NexTerm_${version}_arm64-setup.exe`, "desktop-nsis-windows-arm64"],
    [`NexTerm_${version}_aarch64.dmg`, "desktop-dmg-darwin-arm64"],
    [`NexTerm_${version}_x86_64.dmg`, "desktop-dmg-darwin-amd64"],
    [`NexTerm_${version}_android.apk`, "mobile-android"],
    [`NexTerm_${version}_ios.ipa`, "mobile-ios"],
    [`NexTerm-agent_${version}_windows_amd64.zip`, "agent-zip-windows-amd64"],
    ...["amd64", "arm64"].flatMap((arch) => [
      [`NexTerm-desktop_${version}_linux_${arch}.deb`, `desktop-linux-deb-linux-${arch}`],
      [`NexTerm-server_${version}_linux_${arch}.tar.gz`, `server-archive-linux-${arch}`],
      [`NexTerm-agent_${version}_linux_${arch}.tar.gz`, `agent-archive-linux-${arch}`],
      [`NexTerm-agent_${version}_darwin_${arch}.tar.gz`, `agent-archive-darwin-${arch}`],
    ]),
  ]);
}
