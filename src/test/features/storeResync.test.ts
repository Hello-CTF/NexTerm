/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  sessionApi: { list: vi.fn() },
  assetApi: {},
  dbApi: {},
  terminalApi: { recordingStatus: vi.fn() },
  vaultApi: {},
}));

import { sessionApi, terminalApi, type SessionInfo } from "../../ipc/commands";
import { sanitizeWorkspaceProblemText, useUi } from "../../app/store";
import { deferred } from "./reactTestUtils";

function session(id: string, status: SessionInfo["status"]): SessionInfo {
  return { id, assetId: null, name: id, kind: "ssh", status, tabs: [], createdAt: 1 };
}

describe("resyncSessions", () => {
  beforeEach(() => {
    vi.mocked(sessionApi.list).mockReset();
    vi.mocked(terminalApi.recordingStatus).mockReset();
    vi.spyOn(console, "error").mockImplementation(() => {});
    useUi.setState({ sessions: [], toasts: [], sessionResyncFailed: false, recordings: {} });
  });

  it("replaces local sessions with the authoritative server list", async () => {
    const authoritative = [session("s1", "failed"), session("s2", "connected")];
    vi.mocked(sessionApi.list).mockResolvedValue(authoritative);
    useUi.setState({ sessions: [session("stale", "connected")] });

    await useUi.getState().resyncSessions();

    expect(useUi.getState().sessions).toEqual(authoritative);
  });

  it("preserves existing sessions when fetching fails", async () => {
    vi.mocked(sessionApi.list).mockRejectedValue(new Error("down"));
    const existing = [session("keep", "reconnecting")];
    useUi.setState({ sessions: existing });

    await useUi.getState().resyncSessions();

    expect(useUi.getState().sessions).toEqual(existing);
  });

  it("shows an error toast when fetching fails", async () => {
    vi.mocked(sessionApi.list).mockRejectedValue(new Error("down"));

    await useUi.getState().resyncSessions();

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].kind).toBe("error");
    expect(toasts[0].text).toContain("刷新会话列表失败");
    expect(toasts[0].text).toContain("down");
  });

  it("shows only one toast for consecutive failures", async () => {
    vi.mocked(sessionApi.list).mockRejectedValue(new Error("down"));

    await useUi.getState().resyncSessions();
    await useUi.getState().resyncSessions();
    await useUi.getState().resyncSessions();

    expect(useUi.getState().toasts).toHaveLength(1);
  });

  it("shows a new toast when a failure follows a successful recovery", async () => {
    vi.mocked(sessionApi.list).mockRejectedValueOnce(new Error("down"));
    await useUi.getState().resyncSessions();
    const firstId = useUi.getState().toasts[0]?.id;
    vi.mocked(sessionApi.list).mockResolvedValueOnce([]);
    await useUi.getState().resyncSessions();
    vi.mocked(sessionApi.list).mockRejectedValueOnce(new Error("down"));

    await useUi.getState().resyncSessions();

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].id).not.toBe(firstId);
    expect(toasts[0].text).toContain("刷新会话列表失败");
  });

  it("applies only the latest of two overlapping resyncs", async () => {
    const stale = deferred<SessionInfo[]>();
    vi.mocked(sessionApi.list)
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce([session("fresh", "connected")]);

    const first = useUi.getState().resyncSessions();
    await useUi.getState().resyncSessions();
    stale.resolve([session("stale", "failed")]);
    await first;

    expect(useUi.getState().sessions).toEqual([session("fresh", "connected")]);
  });

  it("neither toasts nor flags failure for a superseded resync that fails late", async () => {
    const stale = deferred<SessionInfo[]>();
    vi.mocked(sessionApi.list)
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce([]);

    const first = useUi.getState().resyncSessions();
    await useUi.getState().resyncSessions();
    stale.reject(new Error("down"));
    await first;

    expect(useUi.getState().sessionResyncFailed).toBe(false);
    expect(useUi.getState().toasts).toEqual([]);
  });
});

describe("sanitizeWorkspaceProblemText", () => {
  it("redacts URL credentials, env assignments, and secrets on 32KiB and 1MiB inputs", () => {
    for (const size of [32 * 1024, 1024 * 1024]) {
      const text = `.env scheme://user:secret@${"a".repeat(size)} FOO=bar ${"b".repeat(size)} password=hunter2`;
      const sanitized = sanitizeWorkspaceProblemText(text);
      expect(sanitized).toContain("scheme://user:[REDACTED]@");
      expect(sanitized).not.toContain(":secret@");
      expect(sanitized).toContain("FOO=[REDACTED]");
      expect(sanitized).toContain("password=[REDACTED]");
      expect(sanitized).not.toContain("hunter2");
    }
  });

  it("keeps env-assignment redaction semantics on quoted and unquoted values", () => {
    const sanitized = sanitizeWorkspaceProblemText(
      '.env A="" B=\'x\' C=bare D="unclosed E=, F=; G=hunter2',
    );
    expect(sanitized).toContain('A=[REDACTED]');
    expect(sanitized).toContain('B=[REDACTED]');
    expect(sanitized).toContain('C=[REDACTED]');
    expect(sanitized).toContain('D=[REDACTED]');
    expect(sanitized).not.toContain("hunter2");
  });

  it("redacts scheme suffixes after digit and punctuation prefixes like the old unanchored regex", () => {
    for (const input of [
      "1http://user:secret@host",
      ".http://user:secret@host",
      "-http://user:secret@host",
      "+http://user:secret@host",
      "9scheme+x://user:secret@host",
    ]) {
      const sanitized = sanitizeWorkspaceProblemText(input);
      expect(sanitized).toContain("user:[REDACTED]@host");
      expect(sanitized).not.toContain(":secret@");
    }
  });

  it("redacts env assignments across full JavaScript whitespace including Unicode spaces", () => {
    const sanitized = sanitizeWorkspaceProblemText(".env A = secret B = hunter2 C  =  hunter3");
    expect(sanitized).toContain("A = [REDACTED]");
    expect(sanitized).toContain("B = [REDACTED]");
    expect(sanitized).toContain("C  =  [REDACTED]");
    expect(sanitized).not.toContain("hunter2");
    expect(sanitized).not.toContain("hunter3");
  });

  it("redacts long values without length truncation", () => {
    const longPassword = "p".repeat(4096);
    const longValue = "v".repeat(4096);
    const sanitized = sanitizeWorkspaceProblemText(
      `scheme://user:${longPassword}@host .env A=${longValue}`,
    );
    expect(sanitized).toContain("user:[REDACTED]@host");
    expect(sanitized).not.toContain(longPassword);
    expect(sanitized).toContain("A=[REDACTED]");
    expect(sanitized).not.toContain(longValue);
  });

  it("scales linearly on long unseparated input instead of regressing to quadratic backtracking", () => {
    const makeInput = (n: number) => `.env ${"a".repeat(n)}`;
    // Minimum-of-N per size: the fastest run approximates the true cost, so scheduler hiccups
    // on the larger measurement cannot masquerade as superlinearity (a real quadratic would
    // show up in every run). Warm-up first so JIT compilation is not measured.
    const measure = (n: number) => {
      let best = Infinity;
      for (let i = 0; i < 5; i++) {
        const t0 = performance.now();
        sanitizeWorkspaceProblemText(makeInput(n));
        best = Math.min(best, performance.now() - t0);
      }
      return best;
    };
    measure(16 * 1024);
    const small = Math.max(measure(32 * 1024), 0.01);
    const large = measure(256 * 1024);
    // 8x input: linear scales about 8x, the old quadratic patterns scaled about 64x.
    // 24 leaves wide headroom while still failing any quadratic regression.
    expect(large / small).toBeLessThan(24);
  });
});

describe("recording resync", () => {
  beforeEach(() => {
    vi.mocked(terminalApi.recordingStatus).mockReset();
    useUi.setState({ recordings: {} });
  });

  it("keeps the newest recording generation", () => {
    useUi.getState().setRecordingStatus({
      tabId: "tab-1",
      state: "failed",
      bytes: 8,
      path: "/tmp/new.log",
      safeError: "disk full",
      generation: 2,
    });
    useUi.getState().setRecordingStatus({
      tabId: "tab-1",
      state: "running",
      bytes: 2,
      path: "/tmp/old.log",
      generation: 1,
    });

    expect(useUi.getState().recordings["tab-1"]?.generation).toBe(2);
    expect(useUi.getState().recordings["tab-1"]?.state).toBe("failed");
  });

  it("drops a stale status response after a newer resync completes", async () => {
    const stale = deferred<{ tabId: string; state: "running"; bytes: number; generation: number }>();
    vi.mocked(terminalApi.recordingStatus)
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce({
        tabId: "tab-1",
        state: "failed",
        bytes: 8,
        path: "/tmp/failed.log",
        safeError: "disk full",
        generation: 1,
      });
    const first = useUi.getState().resyncRecording("tab-1");
    await useUi.getState().resyncRecording("tab-1");

    stale.resolve({ tabId: "tab-1", state: "running", bytes: 2, generation: 1 });
    await first;

    expect(useUi.getState().recordings["tab-1"]?.state).toBe("failed");
    expect(useUi.getState().recordings["tab-1"]?.safeError).toBe("disk full");
  });

  it("clears recording state when sessions reset", () => {
    useUi.getState().setRecordingStatus({
      tabId: "tab-1",
      state: "failed",
      bytes: 8,
      safeError: "disk full",
      generation: 1,
    });

    useUi.getState().resetSessions();

    expect(useUi.getState().recordings).toEqual({});
  });
});
