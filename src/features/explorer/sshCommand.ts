import type { Asset } from "../../ipc/commands";

export function sshCommandCopyable(asset: Pick<Asset, "kind" | "host">): boolean {
  return (asset.kind === "ssh" || asset.kind === "docker") && !!asset.host?.trim();
}

export function formatSshCommand(
  asset: Pick<Asset, "host" | "port" | "username">,
): string {
  const host = (asset.host ?? "").trim();
  const username = (asset.username ?? "").trim();
  const base = username ? `ssh ${username}@${host}` : `ssh ${host}`;
  return asset.port && asset.port !== 22 ? `${base} -p ${asset.port}` : base;
}
