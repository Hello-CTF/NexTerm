import { createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { assetApi } from "../ipc/commands";
import type { AuditEntryDto } from "../ipc/types";
import { AuditView } from "../features/settings/AuditView";
import "../styles.css";

interface HarnessState {
  total: number;
  pages: { offset: number; rows: AuditEntryDto[] }[];
  failNextMoreWith: string | null;
}

const state: HarnessState = { total: 250, pages: [], failNextMoreWith: null };
let gatePromise: Promise<AuditEntryDto[]> | null = null;
let gateResolve: ((rows: AuditEntryDto[]) => void) | null = null;

function makeRows(count: number, startId: number): AuditEntryDto[] {
  return Array.from({ length: count }, (_, i) => ({
    id: startId + i,
    ts: 1700000000000 + (startId + i) * 1000,
    sessionId: null,
    assetId: null,
    source: "user",
    kind: `k${startId + i}`,
    payload: {},
    exitCode: 0,
    durationMs: 1,
  }));
}

assetApi.auditQuery = (args: Record<string, unknown> = {}) => {
  const offset = typeof args.offset === "number" ? args.offset : 0;
  if (offset === 0 && gatePromise) return gatePromise;
  if (offset > 0 && state.failNextMoreWith !== null) {
    const message = state.failNextMoreWith;
    state.failNextMoreWith = null;
    return Promise.reject(new Error(message));
  }
  return Promise.resolve(state.pages.find((p) => p.offset === offset)?.rows ?? []);
};
assetApi.auditCount = () => Promise.resolve({ total: state.total });

const container = document.getElementById("audit-harness-root") as HTMLDivElement;
let root: Root | null = null;

function mount(): void {
  root = createRoot(container);
  root.render(createElement(AuditView));
}

function unmount(): void {
  root?.unmount();
  root = null;
}

function reset(options: {
  total: number;
  pages: { offset: number; rows: AuditEntryDto[] }[];
  gateFirstPage: boolean;
}): void {
  unmount();
  state.total = options.total;
  state.pages = options.pages;
  state.failNextMoreWith = null;
  gatePromise = null;
  gateResolve = null;
  if (options.gateFirstPage) {
    gatePromise = new Promise<AuditEntryDto[]>((resolve) => {
      gateResolve = resolve;
    });
  }
  container.replaceChildren();
}

function releaseFirstPage(): void {
  gateResolve?.(state.pages.find((p) => p.offset === 0)?.rows ?? []);
  gateResolve = null;
  gatePromise = null;
}

function clickButton(label: string): boolean {
  const button = [...document.querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.textContent?.trim() === label,
  );
  if (!button || button.disabled) return false;
  button.click();
  return true;
}

(window as unknown as Record<string, unknown>).__auditHarness = {
  state,
  makeRows,
  reset,
  mount,
  unmount,
  releaseFirstPage,
  clickButton,
};
