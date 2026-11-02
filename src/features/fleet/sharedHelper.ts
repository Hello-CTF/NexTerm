// Two devices of one account that report the same supervisor state digest front one helper: a NexTerm agent and
// the desktop's local-helper exposure on the same machine and OS user. The device list is already one account's,
// and a digest is only reported while the device is online, so offline devices and empty digests never pair.
import type { FleetDevice } from "../../ipc/fleetApi";

// sharedHelperPeers maps a device id to the other devices of the list that front the same helper.
export function sharedHelperPeers(devices: readonly FleetDevice[] | null | undefined): Map<string, FleetDevice[]> {
  const byDigest = new Map<string, FleetDevice[]>();
  for (const device of devices ?? []) {
    const digest = device.agent?.state_digest;
    if (!digest || device.revoked_at !== 0) continue;
    const group = byDigest.get(digest);
    if (group) group.push(device);
    else byDigest.set(digest, [device]);
  }
  const peers = new Map<string, FleetDevice[]>();
  for (const group of byDigest.values()) {
    if (group.length < 2) continue;
    for (const device of group) peers.set(device.id, group.filter((other) => other.id !== device.id));
  }
  return peers;
}

// sharedHelperLabel is the mark shown on a device card; the sessions are listed once, through the agent device.
export function sharedHelperLabel(peers: readonly FleetDevice[] | undefined): string | null {
  if (!peers || peers.length === 0) return null;
  const local = peers.find((peer) => peer.kind === "local-helper");
  if (local) return "与本机终端共用持久终端";
  return `与「${peers[0].name}」共用持久终端`;
}
