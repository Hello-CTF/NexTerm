/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ listenEvent: vi.fn() }));

vi.mock("../ipc/events", () => ({
  EVENTS: { updateProgress: "update://progress" },
  listenEvent: mocks.listenEvent,
}));
vi.mock("../ipc/commands", () => ({
  appUpdateApi: { check: vi.fn(), install: vi.fn(), restart: vi.fn() },
  assetApi: { list: vi.fn(async () => []) },
  layoutApi: { get: vi.fn(async () => ({ revision: 0, updatedAt: 0, data: null })), put: vi.fn() },
  terminalApi: { listLive: vi.fn(async () => []) },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));

import { acquireProgressListener } from "../app/useUpdate";
import { subscribeLayoutChanged } from "../app/layout";

declare const process: {
  on(event: "unhandledRejection", listener: (reason: unknown) => void): void;
  off(event: "unhandledRejection", listener: (reason: unknown) => void): void;
};

const unhandled: unknown[] = [];
function onUnhandledRejection(reason: unknown): void {
  unhandled.push(reason);
}

beforeEach(() => {
  vi.clearAllMocks();
  unhandled.length = 0;
  process.on("unhandledRejection", onUnhandledRejection);
});

afterEach(() => {
  process.off("unhandledRejection", onUnhandledRejection);
});

async function flushRejectionWindow(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
}

describe("async event subscription rejections", () => {
  it("keeps the update progress listener acquirable when the subscription rejects", async () => {
    mocks.listenEvent.mockRejectedValue(new Error("events down"));

    const release = acquireProgressListener();
    await flushRejectionWindow();
    release();

    expect(unhandled).toEqual([]);
    // The reference count unwound, so a later acquire retries the subscription instead of wedging.
    const releaseAgain = acquireProgressListener();
    await flushRejectionWindow();
    releaseAgain();
    expect(mocks.listenEvent).toHaveBeenCalledTimes(2);
    expect(unhandled).toEqual([]);
  });

  it("settles the layout-changed subscription even when listenEvent rejects", async () => {
    mocks.listenEvent.mockRejectedValue(new Error("events down"));

    await expect(subscribeLayoutChanged()).resolves.toBeUndefined();
    await flushRejectionWindow();

    expect(unhandled).toEqual([]);
  });
});
