import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";
import type {
  AiRunDto,
  AiUsageSummaryRow,
  AuditCountDto,
  ImportReport,
  ModelListDto,
  ProviderTestResult,
  SyncBundleWriteResult,
} from "../ipc/types";

beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

describe("demo AI 命令契约", () => {
  it("ai_test_provider 返回与线上一致的 nullable 错误字段", async () => {
    const result = (await mockInvoke("ai_test_provider", { id: "p1" })) as ProviderTestResult;
    expect(result).toEqual({
      modelsOk: true,
      modelsError: null,
      chatOk: true,
      chatError: null,
    });
  });

  it("ai_model_refresh 返回 ModelListDto 对象", async () => {
    const result = (await mockInvoke("ai_model_refresh", { profile: { id: "p1" } })) as ModelListDto;
    expect(Array.isArray(result.models)).toBe(true);
    expect(result.models.length).toBeGreaterThan(0);
    expect(result.malformed).toBe(0);
  });

  it("ai_run_list 返回含生命周期计数字段的 RunDTO", async () => {
    const runs = (await mockInvoke("ai_run_list", { conversationId: "conv-1" })) as AiRunDto[];
    expect(runs.length).toBeGreaterThan(0);
    for (const run of runs) {
      expect(run).toMatchObject({
        id: expect.any(String),
        conversationId: "conv-1",
        status: expect.any(String),
        turns: expect.any(Number),
        tokensIn: expect.any(Number),
        tokensOut: expect.any(Number),
        cacheCreationTokens: expect.any(Number),
        latencyMs: expect.any(Number),
        retries: expect.any(Number),
        failures: expect.any(Number),
      });
    }
    expect(runs.some((r) => r.status === "superseded")).toBe(true);
  });

  it("ai_run_events 接受 limit 分页参数", async () => {
    const events = await mockInvoke("ai_run_events", { jobId: "job-1", afterSeq: 0, limit: 50 });
    expect(Array.isArray(events)).toBe(true);
  });

  it("ai_usage_summary 返回 usage 行", async () => {
    const rows = (await mockInvoke("ai_usage_summary")) as AiUsageSummaryRow[];
    expect(rows.length).toBeGreaterThan(0);
    for (const row of rows) {
      expect(row).toMatchObject({
        source: expect.any(String),
        profileId: expect.any(String),
        runs: expect.any(Number),
        tokensIn: expect.any(Number),
        tokensOut: expect.any(Number),
        cacheCreationTokens: expect.any(Number),
        averageLatencyMs: expect.any(Number),
      });
    }
  });

  it("ai_model_profiles 返回含熔断配置的 profile 视图", async () => {
    const view = (await mockInvoke("ai_model_profiles")) as {
      profiles: Record<string, unknown>[];
      activeId: string | null;
    };
    expect(Array.isArray(view.profiles)).toBe(true);
    for (const profile of view.profiles) {
      expect(profile).toMatchObject({
        id: expect.any(String),
        name: expect.any(String),
        baseUrl: expect.any(String),
        model: expect.any(String),
        temperature: expect.any(Number),
        contextWindow: expect.any(Number),
        stream: expect.any(Boolean),
      });
      for (const key of ["maxTokens", "circuitFailureThreshold", "circuitCooldownSeconds"]) {
        expect(profile[key] === undefined || typeof profile[key] === "number").toBe(true);
      }
    }
  });

  it("audit_count 返回与 audit_query 同过滤的 total", async () => {
    const count = (await mockInvoke("audit_count", { args: { source: "ai" } })) as AuditCountDto;
    const rows = (await mockInvoke("audit_query", { args: { source: "ai", limit: 1000 } })) as unknown[];
    expect(count).toEqual({ total: rows.length });
    expect(count.total).toBeGreaterThan(0);
  });

  it("snippet 命令支持 groupId 与 sort", async () => {
    const created = (await mockInvoke("snippet_create", {
      name: "契约片段",
      body: "echo contract",
      groupId: "g-1",
      sort: 9,
    })) as { id: string };
    expect(created.id).toEqual(expect.any(String));
    const snippets = (await mockInvoke("snippet_list")) as Record<string, unknown>[];
    const mine = snippets.find((s) => s.id === created.id);
    expect(mine).toMatchObject({ groupId: "g-1", sort: 9, name: "契约片段", body: "echo contract" });
    await mockInvoke("snippet_update", { id: created.id, name: "契约片段", body: "echo contract", groupId: null, sort: 1 });
    const after = ((await mockInvoke("snippet_list")) as Record<string, unknown>[]).find(
      (s) => s.id === created.id,
    );
    expect(after).toMatchObject({ groupId: null, sort: 1 });
    await mockInvoke("snippet_delete", { id: created.id });
  });

  it("sync_export 携带 snippets，sync_import 统计片段与凭据墓碑", async () => {
    const bundle = (await mockInvoke("sync_export", {
      args: { assetIds: [], withCreds: false },
    })) as Record<string, unknown>;
    expect(Array.isArray(bundle.snippets)).toBe(true);

    const report = (await mockInvoke("sync_import", {
      args: {
        bundle: {
          protocol: 1,
          origin: "contract-peer",
          exportedAt: Date.now(),
          groups: [],
          assets: [],
          creds: [{ id: "cred-contract", name: "契约凭据", kind: "password", secret: "s3cret" }],
          snippets: [
            { id: "sn-contract", groupId: null, name: "契约片段", body: "echo hi", sort: 0, createdAt: Date.now(), updatedAt: Date.now() },
          ],
          warnings: [],
        },
        force: false,
      },
    })) as ImportReport;
    expect(report).toMatchObject({
      credsCreated: 1,
      credsDeleted: 0,
      snippetsCreated: 1,
      snippetsUpdated: 0,
    });

    const tombstoneReport = (await mockInvoke("sync_import", {
      args: {
        bundle: {
          protocol: 1,
          origin: "contract-peer",
          exportedAt: Date.now(),
          groups: [],
          assets: [],
          creds: [],
          credTombstones: [{ id: "cred-contract", deletedAt: Date.now() + 1000 }],
          snippets: [],
          warnings: [],
        },
        force: false,
      },
    })) as ImportReport;
    expect(tombstoneReport).toMatchObject({ credsDeleted: 1 });
  });

  it("sync_import 报告 skippedNewerDetails 明细", async () => {
    const report = (await mockInvoke("sync_import", {
      args: {
        bundle: {
          protocol: 1,
          origin: "contract-peer",
          exportedAt: Date.now(),
          groups: [],
          assets: [
            { id: "a-web01", groupId: null, kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 1, deletedAt: null },
          ],
          creds: [],
          warnings: [],
        },
        force: false,
      },
    })) as ImportReport;
    expect(report.skippedNewer).toBe(1);
    expect(report.skippedNewerDetails?.[0]).toMatchObject({ kind: "asset", id: "a-web01" });
  });

  it("sync_token 系列在 demo 下与桌面端一致", async () => {
    expect(await mockInvoke("sync_token")).toBeNull();
    expect(await mockInvoke("sync_token_rotate", { args: {} })).toBeNull();
    expect(await mockInvoke("sync_token_rotate", { args: { id: "" } })).toBeNull();
    await expect(
      mockInvoke("sync_token_rotate", { args: { id: "t-1" } }),
    ).rejects.toMatchObject({ code: "unsupported" });
    expect(await mockInvoke("sync_token_list")).toBeNull();
    await expect(mockInvoke("sync_token_issue", { args: { clientId: "box-1" } })).rejects.toMatchObject({
      code: "unsupported",
    });
    await expect(mockInvoke("sync_token_revoke", { args: { id: "t-1" } })).rejects.toMatchObject({
      code: "unsupported",
    });
  });

  it("sync_bundle_write/read 往返并返回加密结果", async () => {
    const plain = (await mockInvoke("sync_bundle_write", {
      args: { path: "demo-bundle.json", content: "{\"protocol\":1}" },
    })) as SyncBundleWriteResult;
    expect(plain).toEqual({
      encrypted: false,
      warning: "资产包以明文导出，获得文件的人都能直接读取其中的凭据，请妥善保管",
    });
    expect(await mockInvoke("sync_bundle_read", { args: { path: "demo-bundle.json" } })).toBe(
      "{\"protocol\":1}",
    );

    const encrypted = (await mockInvoke("sync_bundle_write", {
      args: { path: "demo-bundle.enc.json", content: "{\"protocol\":1}", password: "pw" },
    })) as SyncBundleWriteResult;
    expect(encrypted).toEqual({ encrypted: true });
    await expect(
      mockInvoke("sync_bundle_read", { args: { path: "demo-bundle.enc.json" } }),
    ).rejects.toMatchObject({ code: "bad_param" });
    await expect(
      mockInvoke("sync_bundle_read", { args: { path: "demo-bundle.enc.json", password: "wrong" } }),
    ).rejects.toMatchObject({ code: "decrypt" });
    expect(
      await mockInvoke("sync_bundle_read", { args: { path: "demo-bundle.enc.json", password: "pw" } }),
    ).toBe("{\"protocol\":1}");
  });
});
