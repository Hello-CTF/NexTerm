/** @vitest-environment jsdom */
// FLEET149 接入地址: 客户端校验对齐服务端 normalizeBaseURL 规则;
// 超管有序编辑 (添加/删除/上下移) 经 PUT /fleet/base-urls 保存, 以服务端规范化返回为准。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { click, deferred, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { fetch: vi.fn() };
});

import { validateBaseURL, MAX_BASE_URLS } from "../../features/fleet/baseUrlValidation";
import { BaseUrlsSection } from "../../features/fleet/BaseUrlsSection";

interface RecordedCall {
  url: string;
  method: string;
  body?: unknown;
}

function routePut(response?: { base_urls: { url: string; insecure?: boolean }[] }): RecordedCall[] {
  const calls: RecordedCall[] = [];
  mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
    const u = String(url);
    const method = init.method ?? "GET";
    const body = init.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ url: u, method, ...(body === undefined ? {} : { body }) });
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify(response ?? body),
    };
  });
  vi.stubGlobal("fetch", mocks.fetch);
  return calls;
}

let mounted: MountedView | undefined;

async function expandSection(label: "展开编辑" | "展开" = "展开编辑"): Promise<void> {
  const button = [...document.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label,
  );
  if (!button) throw new Error(`展开按钮未找到: ${label}`);
  click(button);
  await flush();
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("validateBaseURL 对齐服务端规则", () => {
  it("https 地址通过并去尾斜杠", () => {
    expect(validateBaseURL("https://a.example.com/", false)).toEqual({
      url: "https://a.example.com",
      error: null,
    });
    expect(validateBaseURL("  https://a.example.com/base  ", false)).toEqual({
      url: "https://a.example.com/base",
      error: null,
    });
  });

  it("公网明文 http 必须勾选 insecure, 内网/本机放行", () => {
    expect(validateBaseURL("http://nexterm.example.com", false).error).toContain("HTTPS");
    expect(validateBaseURL("http://nexterm.example.com", true).error).toBeNull();
    expect(validateBaseURL("http://192.168.1.8:8080", false).error).toBeNull();
    expect(validateBaseURL("http://localhost:1420", false).error).toBeNull();
    expect(validateBaseURL("http://127.0.0.1", false).error).toBeNull();
  });

  it("拒绝非法 scheme/用户信息/查询参数/片段/空值/超长", () => {
    expect(validateBaseURL("ftp://a.example.com", false).error).toContain("http://");
    expect(validateBaseURL("https://user:pw@a.example.com", false).error).toContain("用户信息");
    expect(validateBaseURL("https://a.example.com?x=1", false).error).toContain("查询参数");
    expect(validateBaseURL("https://a.example.com#frag", false).error).toContain("片段");
    expect(validateBaseURL("   ", false).error).toContain("不能为空");
    expect(validateBaseURL(`https://a.example.com/${"x".repeat(2100)}`, false).error).toContain("不能超过 2048 个字符");
    expect(validateBaseURL("not a url", false).error).toContain("格式无效");
  });
});

describe("BaseUrlsSection 超管编辑", () => {
  const entries = [{ url: "https://a.example.com" }, { url: "https://b.example.com" }];

  it("上下移改顺序, 保存时 PUT 新顺序, 以服务端返回为准", async () => {
    const calls = routePut({ base_urls: [{ url: "https://b.example.com" }, { url: "https://a.example.com" }] });
    let saved: { url: string; insecure?: boolean }[] | null = null;
    mounted = mount(
      createElement(BaseUrlsSection, {
        entries,
        isAdmin: true,
        onSaved: (list) => {
          saved = list;
        },
      }),
    );
    await expandSection();
    await flushUntil(() => document.body.textContent?.includes("#2") ?? false);

    const downButton = document.querySelector<HTMLButtonElement>('button[aria-label="下移 https://a.example.com"]');
    expect(downButton).not.toBeNull();
    click(downButton as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("有未保存的修改") ?? false);

    const saveButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("保存修改"));
    click(saveButton as HTMLButtonElement);
    await flushUntil(() => saved !== null);

    const put = calls.find((c) => c.method === "PUT");
    expect(put?.url).toBe("/fleet/base-urls");
    expect(put?.body).toEqual({ base_urls: [{ url: "https://b.example.com" }, { url: "https://a.example.com" }] });
    expect(saved).toEqual([{ url: "https://b.example.com" }, { url: "https://a.example.com" }]);
    expect(document.body.textContent).not.toContain("有未保存的修改");
  });

  it("添加: 非法地址给行内错误, 重复地址拦截, 合法地址入列", async () => {
    routePut();
    mounted = mount(createElement(BaseUrlsSection, { entries, isAdmin: true, onSaved: () => undefined }));
    await expandSection();
    await flushUntil(() => document.body.textContent?.includes("#2") ?? false);

    const input = document.querySelector<HTMLInputElement>('input[aria-label="新接入地址"]');
    expect(input).not.toBeNull();

    setInputValue(input as HTMLInputElement, "http://public.example.com");
    const addButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "添加");
    click(addButton as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("HTTPS") ?? false);
    expect(document.body.textContent).not.toContain("#3");

    setInputValue(input as HTMLInputElement, "https://a.example.com");
    click(addButton as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("已在列表中") ?? false);

    setInputValue(input as HTMLInputElement, "https://c.example.com/");
    click(addButton as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("https://c.example.com") ?? false);
    expect(document.body.textContent).toContain("#3");
  });

  it("达到上限后添加按钮禁用", async () => {
    routePut();
    const full = Array.from({ length: MAX_BASE_URLS }, (_, i) => ({ url: `https://h${i}.example.com` }));
    mounted = mount(createElement(BaseUrlsSection, { entries: full, isAdmin: true, onSaved: () => undefined }));
    await expandSection();
    await flushUntil(() => document.body.textContent?.includes(`#${MAX_BASE_URLS}`) ?? false);

    const addButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "添加");
    expect((addButton as HTMLButtonElement).disabled).toBe(true);
  });

  it("删除后保存把移除结果发给服务端", async () => {
    const calls = routePut({ base_urls: [{ url: "https://b.example.com" }] });
    mounted = mount(createElement(BaseUrlsSection, { entries, isAdmin: true, onSaved: () => undefined }));
    await expandSection();
    await flushUntil(() => document.body.textContent?.includes("#2") ?? false);

    const removeButton = document.querySelector<HTMLButtonElement>('button[aria-label="删除 https://a.example.com"]');
    click(removeButton as HTMLButtonElement);
    const saveButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("保存修改"));
    await flushUntil(() => !(saveButton as HTMLButtonElement).disabled);
    click(saveButton as HTMLButtonElement);
    await flushUntil(() => calls.some((c) => c.method === "PUT"));

    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ base_urls: [{ url: "https://b.example.com" }] });
  });
});

describe("BaseUrlsSection 普通用户只读", () => {
  it("没有输入框与操作按钮, 提示仅超管可修改", async () => {
    mounted = mount(
      createElement(BaseUrlsSection, { entries: [{ url: "https://a.example.com" }], isAdmin: false, onSaved: () => undefined }),
    );
    await flushUntil(() => document.body.textContent?.includes("1 个地址") ?? false);
    expect(document.body.textContent).not.toContain("#1");

    await expandSection("展开");
    await flushUntil(() => document.body.textContent?.includes("#1") ?? false);
    expect(document.querySelector('input[aria-label="新接入地址"]')).toBeNull();
    expect(document.querySelector('button[aria-label="上移 https://a.example.com"]')).toBeNull();
    expect(document.body.textContent).toContain("仅超级管理员可修改");
  });
});

describe("BaseUrlsSection 默认折叠", () => {
  it("默认只渲染一行摘要, 展开后才出现列表与编辑器", async () => {
    mounted = mount(
      createElement(BaseUrlsSection, {
        entries: [{ url: "https://a.example.com" }, { url: "https://b.example.com" }],
        isAdmin: true,
        onSaved: () => undefined,
      }),
    );
    await flushUntil(() => document.body.textContent?.includes("2 个地址") ?? false);
    expect(document.body.textContent).toContain("https://a.example.com");
    expect(document.querySelector("code")).toBeNull();
    expect(document.querySelector('input[aria-label="新接入地址"]')).toBeNull();
    const toggle = [...document.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "展开编辑",
    );
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");

    await expandSection();
    await flushUntil(() => document.body.textContent?.includes("#2") ?? false);
    expect(document.querySelector('input[aria-label="新接入地址"]')).not.toBeNull();
    const collapse = [...document.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "收起",
    );
    expect(collapse?.getAttribute("aria-expanded")).toBe("true");
  });
});

describe("BaseUrlsSection 空列表引导", () => {
  it("超管看到可操作的空态指引, 普通用户看到联系管理员指引", async () => {
    mounted = mount(createElement(BaseUrlsSection, { entries: [], isAdmin: true, onSaved: () => undefined }));
    await flushUntil(() => document.body.textContent?.includes("未配置") ?? false);
    expect(document.body.textContent).toContain("新设备无法接入");
    await expandSection();
    await flushUntil(() => document.body.textContent?.includes("还没有配置接入地址") ?? false);
    expect(document.body.textContent).toContain("请在下方添加设备能访问到的服务器地址");
    mounted.unmount();

    mounted = mount(createElement(BaseUrlsSection, { entries: [], isAdmin: false, onSaved: () => undefined }));
    await flushUntil(() => document.body.textContent?.includes("请联系超级管理员") ?? false);
    expect(document.body.textContent).toContain("新设备无法接入");
    await expandSection("展开");
    await flushUntil(() => document.body.textContent?.includes("还没有配置接入地址") ?? false);
  });
});

describe("BaseUrlsSection 保存竞态 (R1-5)", () => {
  it("PUT 在途时全部编辑控件锁定, 响应到达后以服务端列表为准", async () => {
    const put = deferred<{ ok: boolean; status: number; text: () => Promise<string> }>();
    mocks.fetch.mockImplementation(async (_url: string, init: RequestInit) => {
      if ((init.method ?? "GET") === "PUT") return put.promise;
      return { ok: true, status: 200, text: async () => JSON.stringify({ base_urls: [] }) };
    });
    vi.stubGlobal("fetch", mocks.fetch);

    mounted = mount(
      createElement(BaseUrlsSection, {
        entries: [{ url: "https://a.example.com" }, { url: "https://b.example.com" }],
        isAdmin: true,
        onSaved: () => undefined,
      }),
    );
    await expandSection();
    await flushUntil(() => document.body.textContent?.includes("#2") ?? false);

    const downButton = document.querySelector<HTMLButtonElement>('button[aria-label="下移 https://a.example.com"]');
    click(downButton as HTMLButtonElement);
    const saveButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("保存修改"));
    await flushUntil(() => !(saveButton as HTMLButtonElement).disabled);
    click(saveButton as HTMLButtonElement);
    await flushUntil(() => document.body.textContent?.includes("保存中") ?? false);

    const input = document.querySelector<HTMLInputElement>('input[aria-label="新接入地址"]');
    const addButton = [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "添加");
    const upButton = document.querySelector<HTMLButtonElement>('button[aria-label="上移 https://b.example.com"]');
    const removeButton = document.querySelector<HTMLButtonElement>('button[aria-label="删除 https://a.example.com"]');
    expect(input?.disabled).toBe(true);
    expect((addButton as HTMLButtonElement).disabled).toBe(true);
    expect(upButton?.disabled).toBe(true);
    expect(removeButton?.disabled).toBe(true);
    expect((saveButton as HTMLButtonElement).disabled).toBe(true);

    put.resolve({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ base_urls: [{ url: "https://b.example.com" }, { url: "https://a.example.com" }] }),
    });
    await flushUntil(() => document.body.textContent?.includes("保存中") === false);

    expect(input?.disabled).toBe(false);
    const order = [...document.querySelectorAll("code")].map((c) => c.textContent);
    expect(order).toEqual(["https://b.example.com", "https://a.example.com"]);
    expect(document.body.textContent).not.toContain("有未保存的修改");
  });
});
