import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useUi, type Workspace } from "../../app/store";
import { fsApi } from "../../ipc/commands";
import {
  EVENTS,
  EventVersionGate,
  listenEvent,
  type TerminalControlEvent,
} from "../../ipc/events";
import { describeError } from "../../ui/errorText";
import { norm } from "./pathUtils";

const STORAGE_KEY = "nexterm.files.followCwd.v1";
const NEVER = "\u0000";

function loadFollowPref(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) !== "0";
  } catch {
    return true;
  }
}

let followEnabled = loadFollowPref();
const prefListeners = new Set<() => void>();

export function isFollowCwdEnabled(): boolean {
  return followEnabled;
}

export function setFollowCwdEnabled(next: boolean): void {
  if (next === followEnabled) return;
  followEnabled = next;
  try {
    localStorage.setItem(STORAGE_KEY, next ? "1" : "0");
  } catch {}
  prefListeners.forEach((l) => l());
}

function subscribePref(cb: () => void): () => void {
  prefListeners.add(cb);
  return () => {
    prefListeners.delete(cb);
  };
}

export function useFollowCwdEnabled(): boolean {
  return useSyncExternalStore(subscribePref, isFollowCwdEnabled);
}

const cwds = new Map<string, string>();
const cwdListeners = new Set<() => void>();
const notifiedFailures = new Set<string>();
let gate = new EventVersionGate();
let listenerStarted = false;
let stopWorkspaceWatch: (() => void) | null = null;
let lastWorkspaces: Workspace[] | null = null;

function noteCwdEvent(p: TerminalControlEvent): void {
  if (p.cwd === undefined) return;
  if (!gate.accept(p.tabId, p.version)) return;
  const prev = cwds.get(p.tabId) ?? null;
  const next = p.cwd || null;
  if (prev === next) return;
  if (next) cwds.set(p.tabId, next);
  else cwds.delete(p.tabId);
  cwdListeners.forEach((l) => l());
}

function pruneCwds(workspaces: Workspace[]): void {
  if (workspaces === lastWorkspaces) return;
  lastWorkspaces = workspaces;
  if (cwds.size === 0) return;
  const live = new Set<string>();
  for (const w of workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind === "terminal" && t.tabId) live.add(t.tabId);
      }
    }
  }
  let changed = false;
  for (const key of cwds.keys()) {
    if (!live.has(key)) {
      cwds.delete(key);
      changed = true;
    }
  }
  if (changed) cwdListeners.forEach((l) => l());
}

function ensureListener(): void {
  if (listenerStarted) return;
  listenerStarted = true;
  void listenEvent<TerminalControlEvent>(EVENTS.terminalControl, noteCwdEvent).catch(() => {
    listenerStarted = false;
  });
  stopWorkspaceWatch = useUi.subscribe((s) => pruneCwds(s.workspaces));
}

function subscribeCwd(cb: () => void): () => void {
  cwdListeners.add(cb);
  ensureListener();
  return () => {
    cwdListeners.delete(cb);
  };
}

export function pickFollowedTabId(workspaces: Workspace[], sessionId: string): string | null {
  for (const w of workspaces) {
    if (w.sessionId !== sessionId) continue;
    const activePane = w.panes.find((p) => p.id === w.activePaneId);
    const panes = activePane ? [activePane, ...w.panes.filter((p) => p !== activePane)] : w.panes;
    for (const p of panes) {
      const ordered = [
        ...p.tabs.filter((t) => t.id === p.activeTabId),
        ...p.tabs.filter((t) => t.id !== p.activeTabId),
      ];
      for (const t of ordered) {
        if (t.kind === "terminal" && t.tabId) return t.tabId;
      }
    }
  }
  return null;
}

export function useFollowedCwd(sessionId: string): string | null {
  const tabId = useUi((s) => pickFollowedTabId(s.workspaces, sessionId));
  return useTerminalCwd(tabId);
}

/** The last directory a terminal reported through OSC 7, by its backend terminal id. */
export function useTerminalCwd(tabId: string | null | undefined): string | null {
  return useSyncExternalStore(subscribeCwd, () => (tabId ? (cwds.get(tabId) ?? null) : null));
}

export function normalizeFollowTarget(cwd: string): string {
  const trimmed = norm(cwd).replace(/\/+$/, "");
  if (trimmed === "") return "/";
  return /^[A-Za-z]:$/.test(trimmed) ? `${trimmed}/` : trimmed;
}

/**
 * Lets an owner (the session side panel) drive the follow instead of the device-wide preference: the owner supplies the
 * terminal directory to follow and keeps the attached state, which user navigation inside the view turns off.
 */
export interface CwdFollowControl {
  /** The followed terminal's directory; null while it has not reported one. */
  cwd: string | null;
  /** True while the view follows the terminal. */
  attached: boolean;
  /** The user navigated inside the view: stop following until attach. */
  detach: () => void;
  /** The user asked to follow the terminal directory again. */
  attach: () => void;
}

// Last applied cwd by memory key, for consumers that remount while their session lives on.
const followMemory = new Map<string, string>();

/**
 * A consumer that remounts (the file tree when its workspace is shown again) passes memoryKey, so the cwd it already followed is
 * not applied again over the user's own navigation and is not listed again on every mount.
 */
export function useCwdFollow(
  sessionId: string,
  current: string,
  memoryKey: string | null,
  apply: (next: string) => void,
  control?: CwdFollowControl,
): {
  supported: boolean;
  enabled: boolean;
  followed: string | null;
  toggle: () => void;
  noteUserNavigation: () => void;
} {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const sessionKind = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.kind);
  const supported = sessionKind === "ssh" || sessionKind === "local";
  const preferenceEnabled = useFollowCwdEnabled();
  const sessionCwd = useFollowedCwd(sessionId);
  const enabled = control ? control.attached : preferenceEnabled;
  const followed = control ? control.cwd : sessionCwd;
  const lastFollowed = useRef<string>((memoryKey && followMemory.get(memoryKey)) || NEVER);
  const applyRef = useRef(apply);
  applyRef.current = apply;
  const currentRef = useRef(current);
  currentRef.current = current;

  useEffect(() => {
    if (!supported || !enabled) return;
    const remember = (value: string) => {
      lastFollowed.current = value;
      if (memoryKey) followMemory.set(memoryKey, value);
    };
    if (!followed) {
      remember(NEVER);
      return;
    }
    if (followed === lastFollowed.current) return;
    remember(followed);
    const next = normalizeFollowTarget(followed);
    if (next === normalizeFollowTarget(currentRef.current)) return;
    let cancelled = false;
    void qc
      .fetchQuery({
        queryKey: ["fs", sessionId, next],
        queryFn: () => fsApi.list(sessionId, next),
        retry: false,
      })
      .then(() => {
        notifiedFailures.delete(sessionId);
        if (!cancelled) applyRef.current(next);
      })
      .catch((e: unknown) => {
        if (cancelled || notifiedFailures.has(sessionId)) return;
        notifiedFailures.add(sessionId);
        pushToast("info", `无法跟随终端切换到 ${next}：${describeError(e)}`);
      });
    return () => {
      cancelled = true;
    };
  }, [supported, enabled, followed, sessionId, memoryKey, qc, pushToast]);

  const controlRef = useRef(control);
  controlRef.current = control;
  const toggle = useCallback(() => {
    const owner = controlRef.current;
    const next = owner ? !owner.attached : !isFollowCwdEnabled();
    if (next) {
      lastFollowed.current = NEVER;
      if (memoryKey) followMemory.delete(memoryKey);
    }
    if (!owner) setFollowCwdEnabled(next);
    else if (next) owner.attach();
    else owner.detach();
  }, [memoryKey]);

  const noteUserNavigation = useCallback(() => {
    const owner = controlRef.current;
    if (owner?.attached) owner.detach();
  }, []);

  return { supported, enabled, followed, toggle, noteUserNavigation };
}

export function resetCwdFollowForTest(): void {
  stopWorkspaceWatch?.();
  stopWorkspaceWatch = null;
  lastWorkspaces = null;
  cwds.clear();
  followMemory.clear();
  notifiedFailures.clear();
  gate = new EventVersionGate();
  followEnabled = loadFollowPref();
  listenerStarted = false;
}

export function cwdFollowMapSizeForTest(): number {
  return cwds.size;
}
