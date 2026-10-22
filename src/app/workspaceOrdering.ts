import { moveItem } from "./tabReorder";
import type { Workspace } from "./store";

export const WORKSPACE_ORDER_LIMIT = 256;

const WORKSPACE_ORDER_VERSION = 1;

export function sanitizeWorkspaceOrder(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  const seen = new Set<string>();
  const ids: string[] = [];
  for (const value of raw) {
    if (typeof value !== "string" || value.length === 0) continue;
    if (seen.has(value)) continue;
    seen.add(value);
    ids.push(value);
    if (ids.length >= WORKSPACE_ORDER_LIMIT) break;
  }
  return ids;
}

export function serializeWorkspaceOrder(ids: readonly string[]): string {
  return JSON.stringify({ v: WORKSPACE_ORDER_VERSION, ids: sanitizeWorkspaceOrder(ids) });
}

export function parseWorkspaceOrder(text: string): string[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return [];
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return [];
  const payload = parsed as Record<string, unknown>;
  if (payload.v !== WORKSPACE_ORDER_VERSION) return [];
  return sanitizeWorkspaceOrder(payload.ids);
}

export function workspaceOrderFromWorkspaces(workspaces: readonly Workspace[]): string[] {
  return workspaces.map((workspace) => workspace.id);
}

export function applyWorkspaceOrder<T extends Workspace>(workspaces: readonly T[], order: readonly string[]): T[] {
  const ids = sanitizeWorkspaceOrder(order);
  if (ids.length === 0) return workspaces as T[];
  const remaining = new Map<string, T[]>();
  for (const workspace of workspaces) {
    const bucket = remaining.get(workspace.id);
    if (bucket) bucket.push(workspace);
    else remaining.set(workspace.id, [workspace]);
  }
  const result: T[] = [];
  const taken = new Set<T>();
  for (const id of ids) {
    const bucket = remaining.get(id);
    const workspace = bucket?.shift();
    if (workspace) {
      result.push(workspace);
      taken.add(workspace);
    }
  }
  for (const workspace of workspaces) {
    if (!taken.has(workspace)) result.push(workspace);
  }
  const unchanged =
    result.length === workspaces.length && result.every((workspace, index) => workspace === workspaces[index]);
  return unchanged ? (workspaces as T[]) : result;
}

export function reorderWorkspaces<T extends Workspace>(workspaces: T[], from: number, to: number): T[] {
  return moveItem(workspaces, from, to);
}
