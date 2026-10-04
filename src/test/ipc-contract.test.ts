import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as ts from "typescript";
import commandsSource from "../ipc/commands.ts?raw";
import cronSource from "../ipc/cron.ts?raw";
import appSource from "../app/App.tsx?raw";

function propertyName(node: ts.PropertyName | undefined): string | undefined {
  return node && (ts.isIdentifier(node) || ts.isStringLiteral(node)) ? node.text : undefined;
}

function commandCalls(fileName: string, fileSource: string) {
  const source = ts.createSourceFile(
    fileName,
    fileSource,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TS,
  );
  const calls: { command: string; nested: boolean }[] = [];
  const visit = (node: ts.Node): void => {
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      node.expression.text === "call" &&
      node.arguments[0] &&
      ts.isStringLiteral(node.arguments[0])
    ) {
      const args = node.arguments[1];
      const nested =
        args !== undefined &&
        ts.isObjectLiteralExpression(args) &&
        args.properties.some(
          (property) =>
            (ts.isPropertyAssignment(property) || ts.isShorthandPropertyAssignment(property)) &&
            propertyName(property.name) === "args",
        ) &&
        args.properties.every((property) => {
          const name = propertyName(property.name);
          return name === "args" || name === "channel";
        });
      calls.push({ command: node.arguments[0].text, nested });
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return calls;
}

describe("IPC facade 静态契约", () => {
  it("保留 153 个方法和 152 个唯一命令", () => {
    const names = commandCalls("commands.ts", commandsSource).map((call) => call.command);
    expect(names).toHaveLength(153);
    expect(new Set(names)).toHaveProperty("size", 152);
    expect(names.filter((name, index) => names.indexOf(name) !== index)).toEqual([
      "ai_presets",
    ]);
  });

  it("保留全部 22 个嵌套 args 命令", () => {
    const nested = commandCalls("commands.ts", commandsSource)
      .filter((call) => call.nested)
      .map((call) => call.command);
    expect(nested).toEqual([
      "session_connect",
      "session_probe",
      "session_probe_host_key",
      "terminal_write",
      "layout_put",
      "asset_create",
      "asset_update",
      "audit_query",
      "asset_probe_batch",
      "fs_write",
      "mount_create",
      "docker_exec_attach",
      "docker_action",
      "db_connect",
      "ai_chat",
      "vault_set_credential",
      "credential_update",
      "sync_export",
      "sync_import",
      "sync_link_set",
      "sync_push",
      "sync_pull",
    ]);
  });

  it("cron 的 5 个命令全部使用扁平 args（拒绝 cron_register 双包装回归）", () => {
    const calls = commandCalls("cron.ts", cronSource);
    expect(calls.map((call) => call.command)).toEqual([
      "cron_register",
      "cron_list",
      "cron_get",
      "cron_set_enabled",
      "cron_unregister",
    ]);
    expect(calls.filter((call) => call.nested)).toEqual([]);
  });

  it("拖动区域内的每个按钮都有 no-drag，双击只匹配目标自身", () => {
    expect(appSource).not.toContain("@tauri-apps");
    expect(appSource).not.toContain("data-tauri-drag-region");
    expect(appSource).not.toContain('closest("[data-wails-drag-region]")');
    expect(appSource).toContain("isWailsDragRegionTarget(event.target)");
    expect(appSource).toContain("onDoubleClick={onDragRegionDoubleClick}");

    const source = ts.createSourceFile(
      "App.tsx",
      appSource,
      ts.ScriptTarget.Latest,
      true,
      ts.ScriptKind.TSX,
    );
    type OpeningElement = ts.JsxOpeningElement | ts.JsxSelfClosingElement;
    const hasAttribute = (node: OpeningElement, name: string) =>
      node.attributes.properties.some(
        (property) =>
          ts.isJsxAttribute(property) &&
          ts.isIdentifier(property.name) &&
          property.name.text === name,
      );
    const hasNoDragStyle = (node: OpeningElement) =>
      node.attributes.properties.some(
        (property) =>
          ts.isJsxAttribute(property) &&
          ts.isIdentifier(property.name) &&
          property.name.text === "style" &&
          property.initializer !== undefined &&
          ts.isJsxExpression(property.initializer) &&
          property.initializer.expression !== undefined &&
          ts.isIdentifier(property.initializer.expression) &&
          property.initializer.expression.text === "wailsNoDragRegionStyle",
      );
    const missingNoDrag: number[] = [];
    let buttons = 0;
    const checkButton = (node: OpeningElement, insideDragRegion: boolean) => {
      if (!ts.isIdentifier(node.tagName) || node.tagName.text !== "button" || !insideDragRegion) {
        return;
      }
      buttons += 1;
      if (!hasNoDragStyle(node)) {
        missingNoDrag.push(source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1);
      }
    };
    const visit = (node: ts.Node, insideDragRegion: boolean): void => {
      if (ts.isJsxElement(node)) {
        const current =
          insideDragRegion || hasAttribute(node.openingElement, "data-wails-drag-region");
        checkButton(node.openingElement, current);
        for (const child of node.children) visit(child, current);
        return;
      }
      if (ts.isJsxSelfClosingElement(node)) {
        const current = insideDragRegion || hasAttribute(node, "data-wails-drag-region");
        checkButton(node, current);
        return;
      }
      ts.forEachChild(node, (child) => visit(child, insideDragRegion));
    };
    visit(source, false);

    expect(buttons).toBe(8);
    expect(missingNoDrag).toEqual([]);
  });
});

function installWebEnv() {
  const storage = new Map<string, string>();
  vi.stubGlobal("window", {
    __NEXTERM_TRANSPORT__: "web",
    location: { search: "", protocol: "http:", host: "127.0.0.1:9" },
    localStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => void storage.set(key, value),
    },
  });
}

function captureRpc() {
  const calls: { url: unknown; cmd?: unknown; args?: unknown }[] = [];
  vi.stubGlobal("fetch", async (...fetchArgs: unknown[]) => {
    const init = fetchArgs[1] as { body?: string } | undefined;
    const body = JSON.parse(String(init?.body)) as { cmd?: unknown; args?: unknown };
    calls.push({ url: fetchArgs[0], ...body });
    return { text: async () => JSON.stringify({ ok: true, data: null }) };
  });
  return calls;
}

function dispatchCronRegister(
  wireArgs: unknown,
): { ok: true; sessionId: string } | { ok: false; code: string } {
  const raw = (wireArgs ?? {}) as Record<string, unknown>;
  const pick = (tag: string) => {
    const key = Object.keys(raw).find(
      (candidate) => candidate.toLowerCase() === tag.toLowerCase(),
    );
    return key === undefined ? undefined : raw[key];
  };
  const sessionId = pick("sessionId");
  if (typeof sessionId !== "string" || sessionId === "") return { ok: false, code: "not_found" };
  return { ok: true, sessionId };
}

describe("cron IPC 线上契约", () => {
  beforeEach(() => {
    vi.resetModules();
    installWebEnv();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("cron_register 线上 args 扁平，旧双包装在 dispatcher 解码下必败", async () => {
    const calls = captureRpc();
    const { cronApi } = await import("../ipc/cron");
    const registration = {
      sessionId: "c-1",
      name: "nightly",
      prompt: "do the thing",
      schedule: "0 0 1 1 *",
      timezone: "UTC",
      timeoutMs: 60_000,
    };

    await cronApi.register(registration);

    expect(calls).toHaveLength(1);
    expect(calls[0]?.url).toBe("/rpc");
    expect(calls[0]?.cmd).toBe("cron_register");
    expect(calls[0]?.args).toEqual(registration);
    expect((calls[0]?.args as Record<string, unknown>).args).toBeUndefined();
    expect(dispatchCronRegister(calls[0]?.args)).toEqual({ ok: true, sessionId: "c-1" });
    expect(dispatchCronRegister({ args: registration })).toEqual({
      ok: false,
      code: "not_found",
    });
  });

  it("cron_list/get/set_enabled/unregister 线上 args 保持扁平", async () => {
    const calls = captureRpc();
    const { cronApi } = await import("../ipc/cron");

    await cronApi.list("c-1");
    await cronApi.get("c-1", "j-1");
    await cronApi.setEnabled("c-1", "j-1", false);
    await cronApi.unregister("c-1", "j-1");

    expect(calls).toEqual([
      { url: "/rpc", cmd: "cron_list", args: { sessionId: "c-1" } },
      { url: "/rpc", cmd: "cron_get", args: { sessionId: "c-1", jobId: "j-1" } },
      { url: "/rpc", cmd: "cron_set_enabled", args: { sessionId: "c-1", jobId: "j-1", enabled: false } },
      { url: "/rpc", cmd: "cron_unregister", args: { sessionId: "c-1", jobId: "j-1" } },
    ]);
  });
});
