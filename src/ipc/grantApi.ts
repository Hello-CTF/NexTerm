import { call } from "./commands";

export type AiGrantKind = "terminal_write" | "session_exec";

export interface AiDeviceGrant {
  deviceId: string;
  kinds: AiGrantKind[];
  updatedAt: number;
}

export const grantApi = {
  list: () => call<AiDeviceGrant[]>("ai_grant_list"),
  set: (deviceId: string, kinds: AiGrantKind[]) =>
    call<AiDeviceGrant>("ai_grant_set", { deviceId, kinds }),
  revoke: (deviceId: string) => call<void>("ai_grant_revoke", { deviceId }),
};
