// Two devices of one account that report the same supervisor state digest and the same helper instance id front one helper: a NexTerm agent and
// the desktop's local-helper exposure on the same machine and OS user. The device list is already one account's,
// and a digest is only reported while the device is online, so offline devices never pair. The digest alone is the same on another
// machine with the same uid and path, so a device without an instance id (older agent or server) never pairs either.
import type { FleetDevice } from "../../ipc/fleetApi";

// sharedHelperPeers maps a device id to the other devices of the list that front the same helper.
export function sharedHelperPeers(devices: readonly FleetDevice[] | null | undefined): Map<string, FleetDevice[]> {
  const byHelper = new Map<string, FleetDevice[]>();
  for (const device of devices ?? []) {
    const digest = device.agent?.state_digest;
    const instance = device.agent?.helper_instance_id;
    if (!digest || !instance || device.revoked_at !== 0) continue;
    const key = JSON.stringify([digest, instance]);
    const group = byHelper.get(key);
    if (group) group.push(device);
    else byHelper.set(key, [device]);
  }
  const peers = new Map<string, FleetDevice[]>();
  for (const group of byHelper.values()) {
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
