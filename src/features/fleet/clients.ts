// fleet/sharing data access switches by transport: WEB goes through the browser HTTP client (session cookie+CSRF
// held by the browser), desktop goes through the remote* proxies (server session via the "account sync" link, cookie+CSRF
// held by the Go side). Method signatures and return types are identical on both sides; the view body does not fork.

import { WEB } from "../../ipc/env";
import { fleetApi } from "../../ipc/fleetApi";
import { sharingApi } from "../../ipc/sharingApi";
import { remoteFleetApi, remoteHealthApi, remoteSharingApi } from "../../ipc/remoteApi";

export type FleetClient = typeof fleetApi;
export type SharingClient = typeof sharingApi;

export function fleetClient(): FleetClient {
  if (WEB) return fleetApi;
  return { ...remoteFleetApi, serverVersion: remoteHealthApi.serverVersion };
}

export function sharingClient(): SharingClient {
  return WEB ? sharingApi : remoteSharingApi;
}
