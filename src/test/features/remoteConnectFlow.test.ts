import { describe, expect, it, vi } from "vitest";
import {
  classifyFailure,
  describeCertificate,
  mayAutoReconnect,
  retryDelayMs,
  runConnectFlow,
  type FlowApi,
  type FlowUi,
} from "../../features/remote/connectFlow";
import { RemoteEngineError, type EngineFailure, type RemoteClient, type RemoteSecret } from "../../features/remote/remoteTypes";
import type { RemoteDesktopCertificateStatus, RemoteDesktopPrepareResult } from "../../ipc/generated/types";

function prepared(patch: Partial<RemoteDesktopPrepareResult> = {}): RemoteDesktopPrepareResult {
  return {
    ticket: "t-1",
    protocol: "rdp",
    assetId: "asset-1",
    name: "win-01",
    host: "10.0.0.5",
    port: 3389,
    username: "admin",
    domain: "CORP",
    promptPassword: false,
    hasPassword: true,
    expiresAtMs: Date.now() + 60_000,
    ...patch,
  };
}

function certificate(patch: Partial<RemoteDesktopCertificateStatus> = {}): RemoteDesktopCertificateStatus {
  return {
    assetId: "asset-1",
    protocol: "rdp",
    host: "10.0.0.5",
    port: 3389,
    status: "new",
    fingerprint: "AA:BB",
    observedAtMs: 1,
    ...patch,
  };
}

const client = { protocol: "rdp", disconnect: vi.fn() } as unknown as RemoteClient;

function setup(overrides: { api?: Partial<FlowApi>; ui?: Partial<FlowUi> } = {}) {
  let counter = 0;
  const api: FlowApi = {
    prepare: vi.fn(async () => prepared({ ticket: `t-${++counter}` })),
    certificate: vi.fn(async () => certificate({ status: "pinned" })),
    trust: vi.fn(async () => undefined),
    setPassword: vi.fn(async () => undefined),
    releaseSecret: vi.fn(async () => ({ username: "admin", domain: "CORP", password: "stored-secret" })),
    ...overrides.api,
  };
  const ui: FlowUi = {
    askPassword: vi.fn(async () => "typed-secret"),
    confirmCertificate: vi.fn(async () => true),
    ...overrides.ui,
  };
  return { api, ui };
}

const rdpFailure = (patch: Partial<EngineFailure>): EngineFailure => ({ protocol: "rdp", stage: "connect", message: "failed", ...patch });

describe("classifyFailure", () => {
  it("treats bad credentials as auth and access denied as permission", () => {
    expect(classifyFailure(rdpFailure({ rdpKind: 1 })).kind).toBe("auth");
    expect(classifyFailure(rdpFailure({ rdpKind: 2 })).kind).toBe("auth");
    expect(classifyFailure(rdpFailure({ rdpKind: 3 })).kind).toBe("permission");
  });

  it("reads an RDCleanPath 403 as a certificate to confirm and a 401 as a stale ticket", () => {
    expect(classifyFailure(rdpFailure({ rdpKind: 4, httpStatus: 403 })).kind).toBe("certificate");
    expect(classifyFailure(rdpFailure({ rdpKind: 4, httpStatus: 401 })).kind).toBe("retryable");
  });

  it("reads the needs_confirm code as a certificate", () => {
    expect(classifyFailure(rdpFailure({ code: "needs_confirm" })).kind).toBe("certificate");
  });

  it("maps vault and account codes to permission", () => {
    expect(classifyFailure(rdpFailure({ code: "vault_locked" })).kind).toBe("permission");
    expect(classifyFailure(rdpFailure({ code: "forbidden" })).kind).toBe("permission");
  });

  it("classifies a vnc security failure by its reason", () => {
    expect(classifyFailure({ protocol: "vnc", stage: "security", message: "VNC 认证失败: Authentication failed" }).kind).toBe("auth");
    expect(classifyFailure({ protocol: "vnc", stage: "security", message: "服务器证书需要确认" }).kind).toBe("certificate");
    expect(classifyFailure({ protocol: "vnc", stage: "security", message: "something else" }).kind).toBe("fatal");
  });

  it("makes only an unclean drop of an established session retryable", () => {
    expect(classifyFailure({ protocol: "vnc", stage: "closed", clean: false, wasConnected: true, message: "x" }).kind).toBe("retryable");
    expect(classifyFailure({ protocol: "vnc", stage: "closed", clean: false, wasConnected: false, message: "x" }).kind).toBe("fatal");
    expect(classifyFailure({ protocol: "vnc", stage: "closed", clean: true, wasConnected: true, message: "x" }).kind).toBe("ended");
  });
});

describe("auto reconnect policy", () => {
  it("backs off and gives up", () => {
    expect(retryDelayMs(0)).toBe(2000);
    expect(retryDelayMs(4)).toBe(30000);
    expect(retryDelayMs(5)).toBeNull();
  });

  it("never retries auth, certificate or permission failures", () => {
    for (const kind of ["auth", "certificate", "permission", "ended", "fatal"] as const) {
      expect(mayAutoReconnect({ kind, message: "x" }, { promptPassword: false, attempt: 0 })).toBe(false);
    }
    expect(mayAutoReconnect({ kind: "retryable", message: "x" }, { promptPassword: false, attempt: 0 })).toBe(true);
  });

  it("waits for the user when the password is asked at connect time", () => {
    expect(mayAutoReconnect({ kind: "retryable", message: "x" }, { promptPassword: true, attempt: 0 })).toBe(false);
  });

  it("stops after the last attempt", () => {
    expect(mayAutoReconnect({ kind: "retryable", message: "x" }, { promptPassword: false, attempt: 5 })).toBe(false);
  });
});

describe("runConnectFlow", () => {
  it("connects an RDP asset with the stored secret from a fresh ticket", async () => {
    const { api, ui } = setup();
    const open = vi.fn(async (_p: RemoteDesktopPrepareResult, _s: RemoteSecret | null) => client);
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result.ok).toBe(true);
    expect(api.releaseSecret).toHaveBeenCalledWith("t-1");
    expect(open.mock.calls[0][0].ticket).toBe("t-1");
    expect(open.mock.calls[0][1]).toEqual({ username: "admin", domain: "CORP", password: "stored-secret" });
    expect(ui.askPassword).not.toHaveBeenCalled();
  });

  it("asks for a new ticket on every attempt", async () => {
    const { api, ui } = setup();
    const open = vi.fn(async () => client);
    await runConnectFlow({ api, ui, open }, "asset-1");
    await runConnectFlow({ api, ui, open }, "asset-1");
    expect(api.prepare).toHaveBeenCalledTimes(2);
    expect(open.mock.calls.map((call) => (call as unknown as [RemoteDesktopPrepareResult])[0].ticket)).toEqual(["t-1", "t-2"]);
  });

  it("prompts for the password of a promptPassword asset and never asks the backend for a stored one", async () => {
    const { api, ui } = setup({ api: { prepare: vi.fn(async () => prepared({ promptPassword: true, hasPassword: false })) } });
    const open = vi.fn(async (_p: RemoteDesktopPrepareResult, _s: RemoteSecret | null) => client);
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result.ok).toBe(true);
    expect(ui.askPassword).toHaveBeenCalledTimes(1);
    expect(api.releaseSecret).not.toHaveBeenCalled();
    expect(open.mock.calls[0][1]).toEqual({ username: "admin", domain: "CORP", password: "typed-secret" });
  });

  it("hands a typed VNC password to the backend only, not to the client", async () => {
    const { api, ui } = setup({ api: { prepare: vi.fn(async () => prepared({ protocol: "vnc", promptPassword: true, hasPassword: false, ticket: "tv" })) } });
    const open = vi.fn(async (_p: RemoteDesktopPrepareResult, _s: RemoteSecret | null) => client);
    await runConnectFlow({ api, ui, open }, "asset-1");
    expect(api.setPassword).toHaveBeenCalledWith("tv", "typed-secret");
    expect(open.mock.calls[0][1]).toBeNull();
  });

  it("stops quietly when the user cancels the password prompt", async () => {
    const { api, ui } = setup({
      api: { prepare: vi.fn(async () => prepared({ hasPassword: false })) },
      ui: { askPassword: vi.fn(async () => null) },
    });
    const open = vi.fn(async () => client);
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result).toMatchObject({ ok: false, canceled: true });
    expect(open).not.toHaveBeenCalled();
  });

  it("shows a new certificate, trusts it on confirmation and retries with a new ticket", async () => {
    const { api, ui } = setup({ api: { certificate: vi.fn(async () => certificate({ status: "new" })) } });
    const open = vi
      .fn<(p: RemoteDesktopPrepareResult, s: RemoteSecret | null) => Promise<RemoteClient>>()
      .mockRejectedValueOnce(new RemoteEngineError(rdpFailure({ rdpKind: 4, httpStatus: 403 })))
      .mockResolvedValueOnce(client);
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result.ok).toBe(true);
    expect(ui.confirmCertificate).toHaveBeenCalledWith(expect.objectContaining({ status: "new", fingerprint: "AA:BB" }));
    expect(api.trust).toHaveBeenCalledWith("asset-1", "AA:BB");
    expect(open.mock.calls.map((call) => call[0].ticket)).toEqual(["t-1", "t-2"]);
  });

  it("shows both fingerprints for a changed certificate", async () => {
    const status = certificate({ status: "changed", fingerprint: "NEW", previousFingerprint: "OLD", subject: "CN=win" });
    const { api, ui } = setup({ api: { certificate: vi.fn(async () => status) } });
    const open = vi
      .fn<(p: RemoteDesktopPrepareResult, s: RemoteSecret | null) => Promise<RemoteClient>>()
      .mockRejectedValueOnce(new RemoteEngineError({ protocol: "rdp", stage: "connect", message: "x", code: "needs_confirm" }))
      .mockResolvedValueOnce(client);
    await runConnectFlow({ api, ui, open }, "asset-1");
    const text = describeCertificate(status);
    expect(text.body).toContain("OLD");
    expect(text.body).toContain("NEW");
    expect(text.body).toContain("警告");
    expect(api.trust).toHaveBeenCalledWith("asset-1", "NEW");
  });

  it("does not trust or retry when the user declines the certificate", async () => {
    const { api, ui } = setup({
      api: { certificate: vi.fn(async () => certificate({ status: "new" })) },
      ui: { confirmCertificate: vi.fn(async () => false) },
    });
    const open = vi.fn(async () => {
      throw new RemoteEngineError(rdpFailure({ rdpKind: 4, httpStatus: 403 }));
    });
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result).toMatchObject({ ok: false, canceled: true, failure: { kind: "certificate" } });
    expect(api.trust).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledTimes(1);
  });

  it("checks the certificate after any VNC security failure", async () => {
    const { api, ui } = setup({
      api: {
        prepare: vi.fn(async () => prepared({ protocol: "vnc", hasPassword: true })),
        certificate: vi.fn(async () => certificate({ protocol: "vnc", status: "new" })),
      },
    });
    const open = vi
      .fn<(p: RemoteDesktopPrepareResult, s: RemoteSecret | null) => Promise<RemoteClient>>()
      .mockRejectedValueOnce(new RemoteEngineError({ protocol: "vnc", stage: "security", message: "首次连接，请确认服务器证书指纹" }))
      .mockResolvedValueOnce(client);
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result.ok).toBe(true);
    expect(api.certificate).toHaveBeenCalledWith("asset-1");
  });

  it("reports an auth failure without retrying it", async () => {
    const { api, ui } = setup();
    const open = vi.fn(async () => {
      throw new RemoteEngineError(rdpFailure({ rdpKind: 1 }));
    });
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result).toMatchObject({ ok: false, failure: { kind: "auth" } });
    expect(open).toHaveBeenCalledTimes(1);
    expect(api.prepare).toHaveBeenCalledTimes(1);
    expect(mayAutoReconnect((result as { failure: { kind: "auth"; message: string } }).failure, { promptPassword: false, attempt: 0 })).toBe(false);
  });

  it("asks once for a VNC password the asset does not hold when the server wants one", async () => {
    const { api, ui } = setup({ api: { prepare: vi.fn(async () => prepared({ protocol: "vnc", hasPassword: false, ticket: "tv" })) } });
    const open = vi
      .fn<(p: RemoteDesktopPrepareResult, s: RemoteSecret | null) => Promise<RemoteClient>>()
      .mockRejectedValueOnce(new RemoteEngineError({ protocol: "vnc", stage: "security", message: "VNC 认证失败: password required" }))
      .mockResolvedValueOnce(client);
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result.ok).toBe(true);
    expect(ui.askPassword).toHaveBeenCalledTimes(1);
    expect(api.prepare).toHaveBeenCalledTimes(2);
  });

  it("reports a prepare failure as a failure of the flow", async () => {
    const { api, ui } = setup({ api: { prepare: vi.fn(async () => Promise.reject({ code: "not_found", message: "资产不存在" })) } });
    const result = await runConnectFlow({ api, ui, open: vi.fn() }, "asset-1");
    expect(result).toMatchObject({ ok: false, failure: { kind: "fatal", message: "资产不存在" } });
  });

  it("does not leave the typed password in anything the flow returns", async () => {
    const { api, ui } = setup({ api: { prepare: vi.fn(async () => prepared({ promptPassword: true })) } });
    const result = await runConnectFlow({ api, ui, open: vi.fn(async () => client) }, "asset-1");
    expect(JSON.stringify(result)).not.toContain("typed-secret");
  });
});

describe("stale tickets", () => {
  const forbidden = () => Promise.reject({ code: "forbidden", message: "票据无效" });

  it("re-prepares once after an RDCleanPath 403 when there is no certificate to confirm, keeping the typed password", async () => {
    const { api, ui } = setup({ api: { prepare: vi.fn(async () => prepared({ promptPassword: true, hasPassword: false, ticket: `t-${++ticketSeq}` })) } });
    ticketSeq = 0;
    const open = vi
      .fn<(p: RemoteDesktopPrepareResult, s: RemoteSecret | null) => Promise<RemoteClient>>()
      .mockRejectedValueOnce(new RemoteEngineError(rdpFailure({ rdpKind: 4, httpStatus: 403 })))
      .mockResolvedValueOnce(client);
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result.ok).toBe(true);
    expect(api.certificate).toHaveBeenCalledTimes(1);
    expect(api.prepare).toHaveBeenCalledTimes(2);
    expect(ui.askPassword).toHaveBeenCalledTimes(1);
    expect(open.mock.calls[1][1]?.password).toBe("typed-secret");
    expect(open.mock.calls.map((call) => call[0].ticket)).toEqual(["t-1", "t-2"]);
  });

  it("gives up after the second 403 instead of looping", async () => {
    const { api, ui } = setup();
    const open = vi.fn(async () => {
      throw new RemoteEngineError(rdpFailure({ rdpKind: 4, httpStatus: 403 }));
    });
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result).toMatchObject({ ok: false, failure: { kind: "fatal" } });
    expect(open).toHaveBeenCalledTimes(2);
    expect((result as { failure: { kind: string } }).failure.kind).not.toBe("certificate");
  });

  it("still asks about a certificate that is new when the 403 comes", async () => {
    const { api, ui } = setup({ api: { certificate: vi.fn(async () => certificate({ status: "changed", previousFingerprint: "OLD" })) } });
    const open = vi
      .fn<(p: RemoteDesktopPrepareResult, s: RemoteSecret | null) => Promise<RemoteClient>>()
      .mockRejectedValueOnce(new RemoteEngineError(rdpFailure({ rdpKind: 4, httpStatus: 403 })))
      .mockResolvedValueOnce(client);
    await runConnectFlow({ api, ui, open }, "asset-1");
    expect(ui.confirmCertificate).toHaveBeenCalledTimes(1);
  });

  it("re-prepares once when setPassword is forbidden, then connects without asking again", async () => {
    const { api, ui } = setup({
      api: {
        prepare: vi.fn(async () => prepared({ protocol: "vnc", promptPassword: true, hasPassword: false, ticket: `tv-${++ticketSeq}` })),
        setPassword: vi.fn().mockImplementationOnce(forbidden).mockResolvedValue(undefined),
      },
    });
    const open = vi.fn(async () => client);
    const result = await runConnectFlow({ api, ui, open }, "asset-1");
    expect(result.ok).toBe(true);
    expect(ui.askPassword).toHaveBeenCalledTimes(1);
    expect(api.setPassword).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.setPassword).mock.calls[1][1]).toBe("typed-secret");
  });

  it("shows a permission error when the second ticket is forbidden too", async () => {
    const { api, ui } = setup({ api: { releaseSecret: vi.fn(forbidden) } });
    const result = await runConnectFlow({ api, ui, open: vi.fn(async () => client) }, "asset-1");
    expect(result).toMatchObject({ ok: false, failure: { kind: "permission" } });
    expect(api.prepare).toHaveBeenCalledTimes(2);
  });

  it("re-prepares once when releaseSecret is forbidden", async () => {
    const { api, ui } = setup({ api: { releaseSecret: vi.fn().mockImplementationOnce(forbidden).mockResolvedValue({ username: "a", domain: "", password: "p" }) } });
    const result = await runConnectFlow({ api, ui, open: vi.fn(async () => client) }, "asset-1");
    expect(result.ok).toBe(true);
    expect(api.prepare).toHaveBeenCalledTimes(2);
  });
});

let ticketSeq = 0;

describe("cancellation", () => {
  it("releases no secret and opens nothing when cancelled while the password prompt is open", async () => {
    let cancelled = false;
    const { api, ui } = setup({
      api: { prepare: vi.fn(async () => prepared({ protocol: "vnc", promptPassword: true })) },
      ui: { askPassword: vi.fn(async () => { cancelled = true; return "typed-secret"; }) },
    });
    const open = vi.fn(async () => client);
    const result = await runConnectFlow({ api, ui, open, cancelled: () => cancelled }, "asset-1");
    expect(result).toMatchObject({ ok: false, canceled: true });
    expect(api.setPassword).not.toHaveBeenCalled();
    expect(api.releaseSecret).not.toHaveBeenCalled();
    expect(open).not.toHaveBeenCalled();
  });

  it("does not release the stored secret when cancelled during prepare", async () => {
    let cancelled = false;
    const { api, ui } = setup({ api: { prepare: vi.fn(async () => { cancelled = true; return prepared(); }) } });
    const result = await runConnectFlow({ api, ui, open: vi.fn(), cancelled: () => cancelled }, "asset-1");
    expect(result).toMatchObject({ ok: false, canceled: true });
    expect(api.releaseSecret).not.toHaveBeenCalled();
  });

  it("closes a client that finished connecting after the cancel", async () => {
    let cancelled = false;
    const disconnect = vi.fn();
    const late = { protocol: "rdp", disconnect } as unknown as RemoteClient;
    const { api, ui } = setup();
    const result = await runConnectFlow({ api, ui, open: vi.fn(async () => { cancelled = true; return late; }), cancelled: () => cancelled }, "asset-1");
    expect(result).toMatchObject({ ok: false, canceled: true });
    expect(disconnect).toHaveBeenCalled();
  });

  it("does not trust or retry after a cancel during the certificate dialog", async () => {
    let cancelled = false;
    const { api, ui } = setup({
      api: { certificate: vi.fn(async () => certificate({ status: "new" })) },
      ui: { confirmCertificate: vi.fn(async () => { cancelled = true; return true; }) },
    });
    const open = vi.fn(async () => { throw new RemoteEngineError(rdpFailure({ rdpKind: 4, httpStatus: 403 })); });
    const result = await runConnectFlow({ api, ui, open, cancelled: () => cancelled }, "asset-1");
    expect(result).toMatchObject({ ok: false, canceled: true });
    expect(api.trust).not.toHaveBeenCalled();
  });
});

describe("prompted flag", () => {
  it("is true when the flow asked for a password, so the drop is not auto-reconnected", async () => {
    const { api, ui } = setup({ api: { prepare: vi.fn(async () => prepared({ hasPassword: false })) } });
    const result = await runConnectFlow({ api, ui, open: vi.fn(async () => client) }, "asset-1");
    expect(result).toMatchObject({ ok: true, prompted: true });
  });

  it("is false when a stored password was used", async () => {
    const { api, ui } = setup();
    const result = await runConnectFlow({ api, ui, open: vi.fn(async () => client) }, "asset-1");
    expect(result).toMatchObject({ ok: true, prompted: false });
  });
});
