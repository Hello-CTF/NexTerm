import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock("../ipc/commands", () => ({ call: mocks.call }));

import { auditTimelineApi } from "../ipc/auditTimelineApi";
import { hostPortsApi } from "../ipc/hostPortsApi";
import { hostProcessesApi } from "../ipc/hostProcessesApi";
import { previewApi } from "../ipc/previewApi";
import { commandSearchApi, transcriptHostSearchApi } from "../ipc/searchApi";

beforeEach(() => {
  mocks.call.mockReset();
});

describe("auditTimelineApi", () => {
  it("passes the request through and maps a page", async () => {
    const items = [{ stream: "audit", id: 1 }];
    const nextCursor = { ts: 5, stream: "audit", id: 1 };
    mocks.call.mockResolvedValue({ items, nextCursor });
    const request = { assetId: "a1", origins: ["ai" as const], limit: 20 };
    await expect(auditTimelineApi.page(request)).resolves.toEqual({ items, nextCursor });
    expect(mocks.call).toHaveBeenCalledWith("audit_timeline", request);
  });

  it("normalizes a missing or partial result to an empty page", async () => {
    mocks.call.mockResolvedValueOnce(null).mockResolvedValueOnce({});
    await expect(auditTimelineApi.page({})).resolves.toEqual({ items: [], nextCursor: null });
    await expect(auditTimelineApi.page({})).resolves.toEqual({ items: [], nextCursor: null });
  });
});

describe("hostPortsApi", () => {
  it("lists listeners and defaults processes to false", async () => {
    const result = { listeners: [], format: "ss", platform: "posix" };
    mocks.call.mockResolvedValue(result);
    await expect(hostPortsApi.list({ sessionId: "s1", quietMs: 500 })).resolves.toBe(result);
    expect(mocks.call).toHaveBeenCalledWith("host_ports_list", { sessionId: "s1", quietMs: 500, processes: false });
    await hostPortsApi.list({ sessionId: "s1", processes: true });
    expect(mocks.call).toHaveBeenLastCalledWith("host_ports_list", { sessionId: "s1", processes: true });
  });

  it("fills unset connectivity fields with empty values", async () => {
    mocks.call.mockResolvedValue({ ran: false, ok: false, command: "x", output: "", durationMs: 0 });
    await hostPortsApi.connectivityTest({ sessionId: "s1", method: "tcp", host: "db", port: 5432, dryRun: true });
    expect(mocks.call).toHaveBeenLastCalledWith("host_connectivity_test", {
      sessionId: "s1", method: "tcp", host: "db", port: 5432, dryRun: true, url: "",
    });
    await hostPortsApi.connectivityTest({ sessionId: "s1", method: "http", url: "http://x" });
    expect(mocks.call).toHaveBeenLastCalledWith("host_connectivity_test", {
      sessionId: "s1", method: "http", url: "http://x", host: "", port: 0,
    });
  });

  it("propagates backend errors", async () => {
    mocks.call.mockRejectedValue(new Error("boom"));
    await expect(hostPortsApi.list({ sessionId: "s1" })).rejects.toThrow("boom");
  });
});

describe("hostProcessesApi", () => {
  it("lists processes for a session", async () => {
    const result = { processes: [], platform: "posix", format: "ps" };
    mocks.call.mockResolvedValue(result);
    await expect(hostProcessesApi.list({ sessionId: "s1" })).resolves.toBe(result);
    expect(mocks.call).toHaveBeenCalledWith("host_processes_list", { sessionId: "s1" });
  });

  it("sends a kill request as a copy and returns the result", async () => {
    const result = { command: "kill -TERM 9", signal: "TERM", ran: true, ok: true, output: "", durationMs: 3 };
    mocks.call.mockResolvedValue(result);
    const request = { sessionId: "s1", pid: 9, ppid: 1, name: "node", started: "t0", dryRun: true };
    await expect(hostProcessesApi.kill(request)).resolves.toBe(result);
    const [command, args] = mocks.call.mock.calls[0];
    expect(command).toBe("host_process_kill");
    expect(args).toEqual(request);
    expect(args).not.toBe(request);
  });
});

describe("previewApi", () => {
  it("checks embeddability through preview_embed_check", async () => {
    const check = { status: "blocked", url: "https://a.test" };
    mocks.call.mockResolvedValue(check);
    await expect(previewApi.embedCheck("https://a.test")).resolves.toBe(check);
    expect(mocks.call).toHaveBeenCalledWith("preview_embed_check", { url: "https://a.test" });
  });
});

describe("commandSearchApi", () => {
  it("sends null filters and zero paging by default", async () => {
    mocks.call.mockResolvedValue({ items: [], hasMore: false });
    await commandSearchApi.search({ text: "ls" });
    expect(mocks.call).toHaveBeenCalledWith("command_search", {
      text: "ls", assetId: null, cwd: null, startedFrom: null, startedTo: null, limit: 0, offset: 0,
    });
  });

  it("keeps given filters and maps items, nulling absent optional fields and dropping extras", async () => {
    mocks.call.mockResolvedValue({
      items: [
        { id: 1, sessionId: "s", tabId: "t", assetId: "a", command: "ls", cwd: "/srv", source: "user", exitCode: 0, startedAt: 1, finishedAt: 2, transcriptId: "tr", extra: "x" },
        { id: 2, sessionId: "s", tabId: "t", assetId: "a", command: "pwd", source: "ai", startedAt: 3, finishedAt: 4 },
      ],
      hasMore: 1,
    });
    const result = await commandSearchApi.search({ text: "l", assetId: "a", cwd: "/srv", startedFrom: 1, startedTo: 9, limit: 10, offset: 5 });
    expect(mocks.call).toHaveBeenCalledWith("command_search", {
      text: "l", assetId: "a", cwd: "/srv", startedFrom: 1, startedTo: 9, limit: 10, offset: 5,
    });
    expect(result.hasMore).toBe(true);
    expect(result.items[0]).not.toHaveProperty("extra");
    expect(result.items[0]).toMatchObject({ exitCode: 0, transcriptId: "tr", cwd: "/srv" });
    expect(result.items[1]).toMatchObject({ exitCode: null, transcriptId: null, cwd: "" });
  });

  it("treats a missing items list as empty", async () => {
    mocks.call.mockResolvedValue({});
    await expect(commandSearchApi.search({ text: "x" })).resolves.toEqual({ items: [], hasMore: false });
  });
});

describe("transcriptHostSearchApi", () => {
  const summary = {
    id: "tr1", sessionId: "s", assetId: "a", assetName: "host", assetKind: "ssh", assetDeleted: false,
    startedAt: 1, endedAt: 2, bytes: 10, storedBytes: 5, chunks: 1, truncated: false, active: false,
    syncOptIn: false, contentOmitted: false,
  };

  it("sends null date bounds by default", async () => {
    mocks.call.mockResolvedValue({ sessions: [], scannedSessions: 0, scannedBytes: 0, truncated: false });
    await transcriptHostSearchApi.search({ assetId: "a", query: "err" });
    expect(mocks.call).toHaveBeenCalledWith("transcript_search_host", {
      assetId: "a", query: "err", startedFrom: null, startedTo: null,
    });
  });

  it("maps sessions and matches, defaulting absent matches and flags", async () => {
    mocks.call.mockResolvedValue({
      sessions: [
        { transcript: { ...summary, unknown: 1 }, matches: [{ seq: 1, ts: 2, preview: "error", extra: 1 }], moreMatches: 1 },
        { transcript: summary },
      ],
      scannedSessions: 2,
      scannedBytes: 20,
    });
    const result = await transcriptHostSearchApi.search({ assetId: "a", query: "err", startedFrom: 1, startedTo: 2 });
    expect(mocks.call).toHaveBeenCalledWith("transcript_search_host", { assetId: "a", query: "err", startedFrom: 1, startedTo: 2 });
    expect(result.scannedSessions).toBe(2);
    expect(result.truncated).toBe(false);
    expect(result.sessions[0].transcript).toEqual(summary);
    expect(result.sessions[0].matches).toEqual([{ seq: 1, ts: 2, preview: "error" }]);
    expect(result.sessions[0].moreMatches).toBe(true);
    expect(result.sessions[1]).toMatchObject({ matches: [], moreMatches: false });
  });
});
