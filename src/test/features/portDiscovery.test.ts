/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ list: vi.fn() }));

vi.mock("../../ipc/hostPortsApi", () => ({ hostPortsApi: { list: mocks.list, connectivityTest: vi.fn() } }));

import {
  acquirePortDiscovery,
  dismissDiscoveredPorts,
  getPortDiscoverySnapshot,
  MAX_FAILURES,
  POLL_INTERVAL_MS,
} from "../../features/preview/portDiscovery";

const l = (port: number, address = "0.0.0.0") => ({ proto: "tcp", address, port });
const result = (...ports: number[]) => ({ listeners: ports.map((p) => l(p)), format: "ss", platform: "posix" });

const releases: Array<() => void> = [];
function acquire(id = "s1") {
  const release = acquirePortDiscovery(id);
  releases.push(release);
  return release;
}
const tick = (ms = POLL_INTERVAL_MS) => vi.advanceTimersByTimeAsync(ms);
const freshPorts = (id = "s1") => getPortDiscoverySnapshot(id).fresh.map((p) => p.port);

beforeEach(() => {
  vi.useFakeTimers();
  mocks.list.mockReset();
});

afterEach(() => {
  for (const r of releases.splice(0)) r();
  vi.useRealTimers();
});

describe("port discovery poller", () => {
  it("does not announce the ports that exist on the first result", async () => {
    mocks.list.mockResolvedValue(result(22, 80));
    acquire();
    await tick(0);
    expect(mocks.list).toHaveBeenCalledWith({ sessionId: "s1", quietMs: 2000 });
    expect(freshPorts()).toEqual([]);
  });

  it("announces a port that appears later and drops it when it closes", async () => {
    mocks.list.mockResolvedValueOnce(result(22)).mockResolvedValueOnce(result(22, 3000)).mockResolvedValue(result(22));
    acquire();
    await tick(0);
    await tick();
    expect(freshPorts()).toEqual([3000]);
    await tick();
    expect(freshPorts()).toEqual([]);
  });

  it("keeps the baseline when the backend skipped the poll", async () => {
    mocks.list
      .mockResolvedValueOnce(result(22))
      .mockResolvedValueOnce({ listeners: [], format: "", platform: "posix", skipped: true })
      .mockResolvedValue(result(22));
    acquire();
    await tick(0);
    await tick();
    await tick();
    expect(freshPorts()).toEqual([]);
  });

  it("does not announce a dismissed port again", async () => {
    mocks.list.mockResolvedValueOnce(result(22)).mockResolvedValue(result(22, 3000));
    acquire();
    await tick(0);
    await tick();
    expect(freshPorts()).toEqual([3000]);
    dismissDiscoveredPorts("s1", [3000]);
    expect(freshPorts()).toEqual([]);
    await tick();
    expect(freshPorts()).toEqual([]);
  });

  it("shares one poller between subscribers of a session", async () => {
    mocks.list.mockResolvedValue(result(22));
    const first = acquire();
    acquire();
    await tick(0);
    expect(mocks.list).toHaveBeenCalledTimes(1);
    first();
    await tick();
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("stops polling and clears state once the last subscriber leaves", async () => {
    mocks.list.mockResolvedValueOnce(result(22)).mockResolvedValue(result(22, 3000));
    const release = acquire();
    await tick(0);
    await tick();
    expect(freshPorts()).toEqual([3000]);
    release();
    expect(freshPorts()).toEqual([]);
    mocks.list.mockClear();
    await tick(POLL_INTERVAL_MS * 3);
    expect(mocks.list).not.toHaveBeenCalled();
  });

  it("stops after consecutive failures", async () => {
    mocks.list.mockRejectedValue(new Error("boom"));
    acquire();
    await tick(0);
    await tick(POLL_INTERVAL_MS * (MAX_FAILURES + 3));
    expect(mocks.list).toHaveBeenCalledTimes(MAX_FAILURES);
  });

  it("does not send a request while one is in flight", async () => {
    mocks.list.mockReturnValue(new Promise(() => undefined));
    acquire();
    await tick(POLL_INTERVAL_MS * 4);
    expect(mocks.list).toHaveBeenCalledTimes(1);
  });
});
