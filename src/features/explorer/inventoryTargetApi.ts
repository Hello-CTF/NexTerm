import { call } from "../../ipc/commands";
import type { InventoryTargetExecRequest } from "../../ipc/generated/types";

export const inventoryTargetApi = {
  execTarget: (request: InventoryTargetExecRequest) =>
    call("inventory_target_exec", { args: request }),
  cancelExec: (request: InventoryTargetExecRequest) =>
    call("inventory_target_exec", { args: { ...request, action: "cancel" } }),
};

export function inventoryExecFailure(cause: unknown): { exitCode: number; output: string } | null {
  if (!cause || typeof cause !== "object") return null;
  const detail = (cause as { detail?: unknown }).detail;
  if (!detail || typeof detail !== "object") return null;
  const { exitCode, output } = detail as { exitCode?: unknown; output?: unknown };
  if (typeof exitCode !== "number" || typeof output !== "string") return null;
  return { exitCode, output };
}

export function inventoryExecCancelled(cause: unknown): boolean {
  if (!cause || typeof cause !== "object") return false;
  const detail = (cause as { detail?: unknown }).detail;
  if (!detail || typeof detail !== "object") return false;
  return (detail as { cancelled?: unknown }).cancelled === true;
}
