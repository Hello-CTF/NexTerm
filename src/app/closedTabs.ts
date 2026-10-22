import { create } from "zustand";

import type { AppTab } from "./store";

export const CLOSED_TAB_STACK_LIMIT = 20;

const CLOSED_TAB_STACK_VERSION = 1;

export type ClosedTabKind = "terminal" | "file" | "detached-terminal";

export type ClosedTabRestoreMode = "reattach" | "reconnect" | "reopen";

type ClosedTabPaneKind = "terminal" | "files" | "log" | "background";

export interface ClosedTabEntry {
  key: string;
  kind: ClosedTabKind;
  tabKind: ClosedTabPaneKind;
  id: string;
  title: string;
  sessionId?: string;
  assetId?: string;
  tabId?: string;
  containerId?: string;
  path?: string;
  workspaceId?: string;
  seq: number;
}

export interface ClosedTabStack {
  accountId: string;
  generation: number;
  nextSeq: number;
  entries: ClosedTabEntry[];
}

export interface RestoredClosedTab {
  mode: ClosedTabRestoreMode;
  tab: AppTab;
}

export function createClosedTabStack(accountId: string, generation = 0): ClosedTabStack {
  return { accountId, generation, nextSeq: 1, entries: [] };
}

function optStr(value: unknown): string | undefined {
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function closedTabKey(tab: AppTab): string | null {
  if (tab.kind === "terminal") {
    if (tab.tabId) return `terminal:tab:${tab.tabId}`;
    return `terminal:id:${tab.id}`;
  }
  if (tab.kind === "files" || tab.kind === "log") {
    if (tab.path) return `file:${tab.kind}:${tab.sessionId ?? ""}:${tab.path}`;
    if (tab.kind === "files" && tab.sessionId) return `files:${tab.sessionId}`;
    return null;
  }
  if (tab.kind === "background") return `background:${tab.id}`;
  return null;
}

export function closedTabEntryFromTab(
  tab: AppTab,
  options?: { workspaceId?: string; detached?: boolean },
): ClosedTabEntry | null {
  const key = closedTabKey(tab);
  if (!key) return null;
  const base = {
    key,
    id: tab.id,
    title: tab.title,
    sessionId: optStr(tab.sessionId),
    assetId: optStr(tab.assetId),
    containerId: optStr(tab.containerId),
    workspaceId: optStr(options?.workspaceId),
    seq: 0,
  };
  if (tab.kind === "terminal") {
    const detachable = options?.detached === true && tab.dead !== true && tab.exited !== true;
    if (detachable && tab.tabId) {
      return { ...base, kind: "detached-terminal", tabKind: "terminal", tabId: tab.tabId };
    }
    return { ...base, kind: "terminal", tabKind: "terminal" };
  }
  if (tab.kind === "background") {
    return { ...base, kind: "detached-terminal", tabKind: "background" };
  }
  return { ...base, kind: "file", tabKind: tab.kind as "files" | "log", path: optStr(tab.path) };
}

export function isClosedTabEntry(value: unknown): value is ClosedTabEntry {
  if (typeof value !== "object" || value === null) return false;
  const entry = value as Record<string, unknown>;
  if (typeof entry.key !== "string" || entry.key.length === 0) return false;
  if (typeof entry.id !== "string" || entry.id.length === 0) return false;
  if (typeof entry.title !== "string" || entry.title.length === 0) return false;
  for (const field of ["sessionId", "assetId", "tabId", "containerId", "path", "workspaceId"] as const) {
    const fieldValue = entry[field];
    if (fieldValue !== undefined && (typeof fieldValue !== "string" || fieldValue.length === 0)) return false;
  }
  const tabId = entry.tabId as string | undefined;
  if (entry.kind === "terminal") {
    return entry.tabKind === "terminal" && tabId === undefined;
  }
  if (entry.kind === "file") {
    if (tabId !== undefined) return false;
    if (entry.tabKind === "files") return true;
    return entry.tabKind === "log" && typeof entry.path === "string";
  }
  if (entry.kind === "detached-terminal") {
    if (entry.tabKind === "terminal") return tabId !== undefined;
    if (entry.tabKind === "background") return tabId === undefined;
    return false;
  }
  return false;
}

function hasValidSeq(entry: ClosedTabEntry): boolean {
  return Number.isSafeInteger(entry.seq) && entry.seq > 0;
}

function restampClosedTabEntries(entries: ClosedTabEntry[]): { entries: ClosedTabEntry[]; nextSeq: number } {
  const total = entries.length;
  return {
    entries: entries.map((entry, index) => ({ ...entry, seq: total - index })),
    nextSeq: total + 1,
  };
}

function canAllocateSeq(stack: ClosedTabStack): boolean {
  return Number.isSafeInteger(stack.nextSeq) && stack.nextSeq >= 1 && stack.nextSeq < Number.MAX_SAFE_INTEGER;
}

export function pushClosedTab(stack: ClosedTabStack, entry: ClosedTabEntry): ClosedTabStack {
  if (!isClosedTabEntry(entry)) return stack;
  const base = canAllocateSeq(stack) ? stack : { ...stack, ...restampClosedTabEntries(stack.entries) };
  const stamped: ClosedTabEntry = { ...entry, seq: base.nextSeq };
  const entries = [stamped, ...base.entries.filter((existing) => existing.key !== entry.key)];
  if (entries.length > CLOSED_TAB_STACK_LIMIT) entries.length = CLOSED_TAB_STACK_LIMIT;
  return { ...base, nextSeq: base.nextSeq + 1, entries };
}

function restoredTab(entry: ClosedTabEntry): RestoredClosedTab {
  const base = {
    id: entry.id,
    title: entry.title,
    sessionId: entry.sessionId,
    assetId: entry.assetId,
    containerId: entry.containerId,
    closable: true,
  };
  if (entry.kind === "detached-terminal" && entry.tabKind === "terminal" && entry.tabId) {
    return {
      mode: "reattach",
      tab: { ...base, kind: "terminal", tabId: entry.tabId },
    };
  }
  if (entry.kind === "detached-terminal") {
    return {
      mode: "reopen",
      tab: { ...base, kind: "background" },
    };
  }
  if (entry.kind === "terminal") {
    return {
      mode: "reconnect",
      tab: { ...base, kind: "terminal", restorePending: true },
    };
  }
  return {
    mode: "reopen",
    tab: { ...base, kind: entry.tabKind, path: entry.path, restorePending: true },
  };
}

export function restoreClosedTab(
  stack: ClosedTabStack,
  key: string,
): { stack: ClosedTabStack; restored: RestoredClosedTab | null } {
  const index = stack.entries.findIndex((entry) => entry.key === key);
  if (index < 0) return { stack, restored: null };
  const entry = stack.entries[index];
  return {
    stack: { ...stack, entries: stack.entries.filter((_, position) => position !== index) },
    restored: restoredTab(entry),
  };
}

export function restoreMostRecentClosedTab(stack: ClosedTabStack): {
  stack: ClosedTabStack;
  restored: RestoredClosedTab | null;
} {
  const latest = stack.entries[0];
  if (!latest) return { stack, restored: null };
  return restoreClosedTab(stack, latest.key);
}

// Generations must stay unique across an account switch so stale callbacks from a
// previous epoch are never accepted again; once the safe-integer space is exhausted
// no fresh generation exists, so fail closed instead of wrapping or reusing one.
function nextGeneration(generation: number): number {
  if (!Number.isSafeInteger(generation) || generation >= Number.MAX_SAFE_INTEGER) {
    throw new RangeError("closed-tab account generation exhausted");
  }
  return generation + 1;
}

export function switchClosedTabAccount(stack: ClosedTabStack, accountId: string): ClosedTabStack {
  if (accountId === stack.accountId) return stack;
  return { accountId, generation: nextGeneration(stack.generation), nextSeq: stack.nextSeq, entries: [] };
}

interface ClosedTabStackPayload {
  v: number;
  accountId: string;
  generation: number;
  nextSeq: number;
  entries: ClosedTabEntry[];
}

export function serializeClosedTabStack(stack: ClosedTabStack): string {
  const payload: ClosedTabStackPayload = {
    v: CLOSED_TAB_STACK_VERSION,
    accountId: stack.accountId,
    generation: stack.generation,
    nextSeq: stack.nextSeq,
    entries: stack.entries,
  };
  return JSON.stringify(payload);
}

function nonNegativeInt(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : fallback;
}

export function deserializeClosedTabStack(raw: string, accountId: string): ClosedTabStack {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return createClosedTabStack(accountId);
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return createClosedTabStack(accountId);
  const payload = parsed as Record<string, unknown>;
  if (payload.v !== CLOSED_TAB_STACK_VERSION || typeof payload.accountId !== "string") {
    return createClosedTabStack(accountId);
  }
  const generation = nonNegativeInt(payload.generation, 0);
  if (payload.accountId !== accountId) {
    return { accountId, generation: nextGeneration(generation), nextSeq: 1, entries: [] };
  }
  const rawEntries = Array.isArray(payload.entries) ? payload.entries : [];
  const seenKeys = new Set<string>();
  const entries = rawEntries
    .filter((entry): entry is ClosedTabEntry => isClosedTabEntry(entry) && hasValidSeq(entry as ClosedTabEntry))
    .filter((entry) => {
      if (seenKeys.has(entry.key)) return false;
      seenKeys.add(entry.key);
      return true;
    })
    .slice(0, CLOSED_TAB_STACK_LIMIT)
    .map((entry) => ({ ...entry }));
  const maxSeq = entries.reduce((max, entry) => Math.max(max, entry.seq), 0);
  if (maxSeq >= Number.MAX_SAFE_INTEGER) {
    return { accountId, generation, ...restampClosedTabEntries(entries) };
  }
  return {
    accountId,
    generation,
    nextSeq: Math.max(nonNegativeInt(payload.nextSeq, 1), maxSeq + 1, 1),
    entries,
  };
}

const CLOSED_TAB_STORAGE_PREFIX = "nexterm:closed-tabs-v1:";

function closedTabStorageKey(accountId: string): string {
  return `${CLOSED_TAB_STORAGE_PREFIX}${encodeURIComponent(accountId)}`;
}

function loadPersistedClosedTabStack(accountId: string): ClosedTabStack {
  try {
    const raw = window.localStorage.getItem(closedTabStorageKey(accountId));
    if (!raw) return createClosedTabStack(accountId);
    return deserializeClosedTabStack(raw, accountId);
  } catch {
    return createClosedTabStack(accountId);
  }
}

function persistClosedTabStack(stack: ClosedTabStack): void {
  try {
    window.localStorage.setItem(closedTabStorageKey(stack.accountId), serializeClosedTabStack(stack));
  } catch {
  }
}

interface ClosedTabsState {
  stack: ClosedTabStack;
  // ensureAccount is called at layout boot and on every account switch; it is a no-op
  // while the account is unchanged so in-flight captures keep their generation guard.
  ensureAccount: (accountId: string) => void;
  // capture rejects entries stamped with a stale generation so a close that resolves
  // after an account switch can never write into the next account's stack.
  capture: (entry: ClosedTabEntry, expectedGeneration: number) => boolean;
  restore: (key?: string) => RestoredClosedTab | null;
  resetForTest: () => void;
}

export const useClosedTabs = create<ClosedTabsState>((set, get) => ({
  stack: createClosedTabStack(""),
  ensureAccount: (accountId) => {
    const { stack } = get();
    if (stack.accountId === accountId) return;
    const switched = switchClosedTabAccount(stack, accountId);
    const persisted = loadPersistedClosedTabStack(accountId);
    set({
      stack: {
        ...switched,
        generation: Math.max(switched.generation, persisted.generation),
        nextSeq: Math.max(switched.nextSeq, persisted.nextSeq),
        entries: persisted.entries,
      },
    });
  },
  capture: (entry, expectedGeneration) => {
    const { stack } = get();
    if (stack.generation !== expectedGeneration) return false;
    const next = pushClosedTab(stack, entry);
    if (next === stack) return false;
    set({ stack: next });
    persistClosedTabStack(next);
    return true;
  },
  restore: (key) => {
    const { stack } = get();
    const result = key === undefined ? restoreMostRecentClosedTab(stack) : restoreClosedTab(stack, key);
    if (!result.restored) return null;
    set({ stack: result.stack });
    persistClosedTabStack(result.stack);
    return result.restored;
  },
  resetForTest: () => set({ stack: createClosedTabStack("") }),
}));
