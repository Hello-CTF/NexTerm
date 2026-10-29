import { useRef } from "react";
import { useShallow } from "zustand/react/shallow";
import type { SessionRuntimeInfo, UiState, Workspace } from "./types";
import { sessionStatusText } from "./sessionStatus";
import { useUi } from "./useUi";

// Selector conventions. A component re-renders whenever its selector result changes identity, and the
// session list is rebuilt on every status event and resync. Components therefore subscribe to the
// smallest value they render: a primitive, one session (useSessionById), or a derived value kept stable
// by useUiStable. Whole-collection selectors such as `useUi((s) => s.sessions)` are rejected by lint;
// views that really list every session or lay out every workspace use useSessionList/useWorkspaceList,
// which keep that choice visible. High-frequency terminal view data (throughput, throttle, cwd, title)
// lives in terminalViewStore, not here.

// sameData compares plain store data structurally: primitives by Object.is, arrays and plain objects by
// their entries. Functions and other instances compare by identity.
export function sameData(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;
  if (Array.isArray(a)) {
    if (!Array.isArray(b) || a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (!sameData(a[i], b[i])) return false;
    return true;
  }
  if (Array.isArray(b)) return false;
  const protoA = Object.getPrototypeOf(a);
  if ((protoA !== Object.prototype && protoA !== null) || Object.getPrototypeOf(b) !== protoA) return false;
  const keysA = Object.keys(a);
  if (keysA.length !== Object.keys(b).length) return false;
  for (const key of keysA) {
    if (!Object.prototype.hasOwnProperty.call(b, key)) return false;
    if (!sameData((a as Record<string, unknown>)[key], (b as Record<string, unknown>)[key])) return false;
  }
  return true;
}

// useUiStable subscribes to a derived value and keeps returning the previous result while isEqual holds,
// so rebuilding the same data does not re-render the caller.
export function useUiStable<T>(selector: (state: UiState) => T, isEqual: (a: T, b: T) => boolean = sameData): T {
  const previous = useRef<{ value: T } | null>(null);
  return useUi((state) => {
    const next = selector(state);
    const held = previous.current;
    if (held && isEqual(held.value, next)) return held.value;
    previous.current = { value: next };
    return next;
  });
}

// useSessionById follows one session; other sessions changing, or a resync returning the same data,
// does not re-render the caller.
export function useSessionById(id: string | null | undefined): SessionRuntimeInfo | undefined {
  return useUiStable((state) => (id ? state.sessions.find((session) => session.id === id) : undefined));
}

export function useSessionStatus(id: string | null | undefined): SessionRuntimeInfo["status"] | undefined {
  return useUi((state) => (id ? state.sessions.find((session) => session.id === id)?.status : undefined));
}

export function useSessionLiveness(id: string | null | undefined): string | null | undefined {
  return useUi((state) => (id ? state.sessions.find((session) => session.id === id)?.liveness : undefined));
}

export function useHasSessions(): boolean {
  return useUi((state) => state.sessions.length > 0);
}

export interface SessionSummary {
  // total and connected count the sessions the open workspaces use.
  total: number;
  connected: number;
  // idle counts connected sessions no open workspace uses and that hold no terminal: what is left of a closed
  // workspace until the idle sweep disconnects it. background counts the other connected sessions outside the open
  // workspaces, such as the connection of a terminal moved to the background.
  idle: number;
  background: number;
  // detail lists the session names under each count, one line per group, for the status bar's tooltip. It is a
  // string so an unchanged list keeps the summary equal and the status bar still.
  detail: string;
}

// useSessionSummary is the status bar's view of the session list: it re-renders only when a count changes.
export function useSessionSummary(): SessionSummary {
  return useUi(useShallow((state) => {
    const used = new Set<string>();
    for (const workspace of state.workspaces) {
      if (workspace.sessionId) used.add(workspace.sessionId);
      for (const pane of workspace.panes) {
        for (const tab of pane.tabs) if (tab.sessionId) used.add(tab.sessionId);
      }
    }
    let total = 0;
    let connected = 0;
    let idle = 0;
    let background = 0;
    const inUse: string[] = [];
    const idleNames: string[] = [];
    const backgroundNames: string[] = [];
    for (const session of state.sessions) {
      if (used.has(session.id)) {
        total += 1;
        if (session.status === "connected") connected += 1;
        inUse.push(session.status === "connected" ? session.name : `${session.name}（${sessionStatusText(session.status)}）`);
      } else if (session.status === "connected") {
        if (session.kind !== "remote" && (session.tabs?.length ?? 0) === 0) {
          idle += 1;
          idleNames.push(session.name);
        } else {
          background += 1;
          backgroundNames.push(session.name);
        }
      }
    }
    const lines = [
      inUse.length > 0 ? `使用中：${inUse.join("、")}` : "",
      idleNames.length > 0 ? `空闲：${idleNames.join("、")}` : "",
      backgroundNames.length > 0 ? `后台：${backgroundNames.join("、")}` : "",
    ].filter(Boolean);
    return { total, connected, idle, background, detail: lines.join("\n") };
  }));
}

// useSessionList is for views that list sessions. With ids it follows only those sessions; without, the
// whole list. Either way a resync that returns equal data keeps the previous array.
export function useSessionList(ids?: readonly (string | null | undefined)[]): SessionRuntimeInfo[] {
  const key = ids?.filter((id): id is string => Boolean(id));
  return useUiStable((state) => (key ? state.sessions.filter((session) => key.includes(session.id)) : state.sessions));
}

// useWorkspaceList is for views that lay out or list every workspace. It follows any workspace change,
// so per-workspace views should select their own workspace instead.
export function useWorkspaceList(): Workspace[] {
  return useUi(selectWorkspaces);
}

function selectWorkspaces(state: UiState): Workspace[] {
  return state.workspaces;
}

// useWorkspaceById follows one workspace by reference.
export function useWorkspaceById(id: string | null | undefined): Workspace | undefined {
  return useUi((state) => (id ? state.workspaces.find((workspace) => workspace.id === id) : undefined));
}
