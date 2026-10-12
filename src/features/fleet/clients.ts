// fleet/sharing 数据访问按传输切换: WEB 走浏览器 HTTP 客户端 (会话 cookie+CSRF
// 由浏览器持有), 桌面走 remote* 代理 (经「账号同步」link 的服务端会话, cookie+CSRF
// 由 Go 侧持有)。两侧方法签名与返回类型同形, 视图主体不分叉。

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
