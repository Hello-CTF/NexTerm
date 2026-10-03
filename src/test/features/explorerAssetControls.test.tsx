/** @vitest-environment jsdom */

// M59：资产区控件（分组改名/删除、测试连接、命令片段）的显式动作与护栏测试。
//
// 全部走 mission-local mock（vi.mock 本文件内的模块），不动共享 IPC/demo 文件。
// 覆盖点：显式动作才触发写操作、确认框可取消、错误行内展示 + 重试、
// 探测的防连点/防陈旧/不带凭据、片段插入只写入不执行、加载/空/错三态。
//
// 时序约定（与 reactTestUtils 的语义一致）：
// · 初始渲染靠 react-query 驱动，waitFor 等文本出现即可；
// · 点击触发的组件内异步（probe/save/delete） resolve 后，DOM 更新要等
//   一个完整 act 退出才落盘 —— 所以先 waitFor 断言 mock 调用，再 flush() 后断言 DOM。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  deferred,
  flush,
  mount,
  setInputValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  groupList: vi.fn(),
  groupUpdate: vi.fn(),
  groupDelete: vi.fn(),
  snippetList: vi.fn(),
  snippetCreate: vi.fn(),
  snippetUpdate: vi.fn(),
  snippetDelete: vi.fn(),
  listCredentials: vi.fn(),
  probe: vi.fn(),
  write: vi.fn(),
  assetDelete: vi.fn(),
  ask: vi.fn(),
  pickKeyFile: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, pickKeyFile: mocks.pickKeyFile }));
vi.mock("../../ipc/webFiles", () => ({
  browserFilesAvailable: () => false,
  pickBrowserFile: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
  assetApi: {
    list: mocks.list,
    search: vi.fn(),
    update: vi.fn(),
    delete: mocks.assetDelete,
    groupList: mocks.groupList,
    groupUpdate: mocks.groupUpdate,
    groupDelete: mocks.groupDelete,
    snippetList: mocks.snippetList,
    snippetCreate: mocks.snippetCreate,
    snippetUpdate: mocks.snippetUpdate,
    snippetDelete: mocks.snippetDelete,
    readKeyFile: vi.fn(),
  },
  vaultApi: {
    listCredentials: mocks.listCredentials,
    setCredential: vi.fn(),
  },
  sessionApi: {
    probe: mocks.probe,
    connect: vi.fn(),
    list: vi.fn(),
  },
  terminalApi: { write: mocks.write },
  dbApi: {},
}));

import { AssetTree, AssetEditor } from "../../features/explorer/AssetTree";
import { SnippetsPanel } from "../../features/explorer/SnippetsPanel";
import { useUi } from "../../app/store";

const GROUP = {
  id: "g1",
  parentId: null,
  name: "生产",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
};
const SNIPPETS = [
  { id: "sn1", name: "看容器状态", body: "docker ps", groupId: null, sort: 1 },
  { id: "sn2", name: "磁盘水位", body: "df -h", groupId: null, sort: 2 },
];

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function buttonByTitle(container: ParentNode, title: string): HTMLButtonElement {
  const button = [...container.querySelectorAll("button")].find(
    (candidate) => candidate.title === title,
  );
  if (!button) throw new Error(`Button not found by title: ${title}`);
  return button;
}

function setTerminalWorkspace(tabId: string | null) {
  useUi.setState({
    workspaces: tabId
      ? [
          {
            id: "ws1",
            kind: "session" as const,
            title: "ws",
            closable: true,
            panes: [
              {
                id: "p1",
                activeTabId: "t1",
                tabs: [
                  {
                    id: "t1",
                    kind: "terminal" as const,
                    title: "终端 1",
                    tabId,
                    closable: true,
                  },
                ],
              },
            ],
            activePaneId: "p1",
            splitRatio: 0.5,
          },
        ]
      : [],
    activeWorkspaceId: tabId ? "ws1" : null,
  });
}

let mounted: MountedView | undefined;
beforeEach(() => {
  // reset（而非 clear）：连 once 队列一起清掉，避免上一个用例没吃完的
  // mockResolvedValueOnce 漏进下一个用例的 ask 队列
  vi.resetAllMocks();
  document.body.replaceChildren();
  mocks.list.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([{ ...GROUP }]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue(SNIPPETS.map((s) => ({ ...s })));
  mocks.ask.mockResolvedValue(true);
  setTerminalWorkspace(null);
  useUi.setState({ leftOpen: true, pushToast: mocks.toast });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

// ───────── 分组：改名 / 删除 ─────────

describe("asset group controls", () => {
  it("renames only through the explicit dialog — never on row selection", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产"));

    // 选中/展开分组（点行首的折叠按钮）不得触发任何写操作
    const toggle = [...mounted!.container.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("生产"),
    );
    if (!toggle) throw new Error("Group row toggle not found");
    click(toggle);
    expect(mocks.groupUpdate).not.toHaveBeenCalled();
    expect(mocks.groupDelete).not.toHaveBeenCalled();

    click(buttonByTitle(mounted!.container, "重命名分组"));
    const input = mounted!.container.querySelector<HTMLInputElement>(".nx-modal input.nx-input");
    if (!input) throw new Error("Rename input not found");
    expect(input.value).toBe("生产");
    setInputValue(input, "生产环境");
    clickButton(mounted!.container, "保存");
    await waitFor(() => expect(mocks.groupUpdate).toHaveBeenCalledWith("g1", "生产环境"));
    // 成功后弹窗关闭
    await flush();
    expect(mounted!.container.querySelector(".nx-modal input.nx-input")).toBeNull();
  });

  it("keeps a rename failure inline with a working retry", async () => {
    mocks.groupUpdate
      .mockRejectedValueOnce({ code: "internal", message: "boom" })
      .mockResolvedValueOnce({ ...GROUP, name: "生产环境" });
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产"));

    click(buttonByTitle(mounted!.container, "重命名分组"));
    const input = mounted!.container.querySelector<HTMLInputElement>(".nx-modal input.nx-input");
    if (!input) throw new Error("Rename input not found");
    setInputValue(input, "生产环境");
    clickButton(mounted!.container, "保存");
    await waitFor(() => expect(mocks.groupUpdate).toHaveBeenCalledTimes(1));
    // 失败错误留在弹窗里（不关闭），可直接改完再点保存重试
    await flush();
    expect(mounted!.container.textContent).toContain("boom");
    expect(mounted!.container.querySelector(".nx-modal input.nx-input")).not.toBeNull();

    clickButton(mounted!.container, "保存");
    await waitFor(() => expect(mocks.groupUpdate).toHaveBeenCalledTimes(2));
    await flush();
    expect(mounted!.container.querySelector(".nx-modal input.nx-input")).toBeNull();
  });

  it("deletes a group only after the confirmation resolves true", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted!.container, "删除分组"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("生产"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.groupDelete).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted!.container, "删除分组"));
    await waitFor(() => expect(mocks.groupDelete).toHaveBeenCalledWith("g1"));
    expect(mocks.ask).toHaveBeenCalledTimes(2);
  });

  it("shows a delete failure inline and retries without re-asking", async () => {
    mocks.groupDelete
      .mockRejectedValueOnce({ code: "internal", message: "disk on fire" })
      .mockResolvedValueOnce(undefined);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产"));

    click(buttonByTitle(mounted!.container, "删除分组"));
    await waitFor(() => expect(mocks.groupDelete).toHaveBeenCalledTimes(1));
    await flush();
    expect(mounted!.container.textContent).toContain("disk on fire");
    expect(mocks.ask).toHaveBeenCalledTimes(1);

    clickButton(mounted!.container, "重试");
    await waitFor(() => expect(mocks.groupDelete).toHaveBeenCalledTimes(2));
    expect(mocks.ask).toHaveBeenCalledTimes(1); // 重试不再弹确认
    await flush();
    expect(mounted!.container.textContent).not.toContain("disk on fire");
  });
});

// ───────── 资产编辑器：测试连接 ─────────

async function mountEditor(): Promise<void> {
  mounted = mountWithClient(
    createElement(AssetEditor, { kind: "asset", onClose: vi.fn(), onSaved: vi.fn() }),
  );
  await flush();
}

async function setHost(value: string): Promise<void> {
  const host = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="1.2.3.4"]');
  if (!host) throw new Error("Host input not found");
  setInputValue(host, value);
}

describe("asset editor test connection", () => {
  it("probes host/port with an explicit bounded timeout and no credentials", async () => {
    await mountEditor();
    await setHost("10.0.0.8");
    // 顺手在密码框里填一个「诱饵」：探测调用不得携带它
    const password = [
      ...mounted!.container.querySelectorAll<HTMLInputElement>('input[type="password"]'),
    ][0];
    if (password) setInputValue(password, "hunter2");

    mocks.probe.mockResolvedValue({ open: true });
    clickButton(mounted!.container, "测试连接");
    await waitFor(() => expect(mocks.probe).toHaveBeenCalledTimes(1));
    // 有界探测：host + port + 显式 3s 超时，参数里没有任何凭据
    expect(mocks.probe).toHaveBeenCalledWith("10.0.0.8", 22, 3000);
    expect(mocks.probe.mock.calls[0]).toHaveLength(3);
    await flush();
    expect(mounted!.container.textContent).toContain("端口可连接");
  });

  it("does not double-submit while a probe is in flight", async () => {
    await mountEditor();
    await setHost("10.0.0.8");

    const pending = deferred<{ open: boolean }>();
    mocks.probe.mockReturnValue(pending.promise);
    clickButton(mounted!.container, "测试连接");
    clickButton(mounted!.container, "测试连接");
    clickButton(mounted!.container, "测试连接");
    await flush();
    expect(mocks.probe).toHaveBeenCalledTimes(1);

    pending.resolve({ open: true });
    await flush();
    expect(mounted!.container.textContent).toContain("端口可连接");
  });

  it("discards a stale result when the target changes mid-flight", async () => {
    await mountEditor();
    await setHost("10.0.0.8");

    const pending = deferred<{ open: boolean }>();
    mocks.probe.mockReturnValue(pending.promise);
    clickButton(mounted!.container, "测试连接");
    await flush();

    // 探测在途时用户改了主机：旧结果描述的是旧目标，必须被丢弃
    await setHost("10.0.0.9");
    pending.resolve({ open: true });
    await flush();
    expect(mounted!.container.textContent).not.toContain("端口可连接");
  });

  it("surfaces a closed port with the kernel error text", async () => {
    await mountEditor();
    await setHost("10.0.0.8");

    mocks.probe.mockResolvedValue({ open: false, error: "connection refused" });
    clickButton(mounted!.container, "测试连接");
    await waitFor(() => expect(mocks.probe).toHaveBeenCalledTimes(1));
    await flush();
    expect(mounted!.container.textContent).toContain("不可连接:connection refused");
  });
});

// ───────── 命令片段 ─────────

describe("snippets panel", () => {
  it("renders loading, then the list", async () => {
    const pending = deferred<typeof SNIPPETS>();
    mocks.snippetList.mockReturnValue(pending.promise);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    expect(mounted.container.textContent).toContain("正在加载片段");
    pending.resolve(SNIPPETS.map((s) => ({ ...s })));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));
    expect(mounted.container.textContent).toContain("磁盘水位");
  });

  it("renders the empty state", async () => {
    mocks.snippetList.mockResolvedValue([]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("还没有片段"));
  });

  it("surfaces a list failure with a retry that refetches", async () => {
    mocks.snippetList
      .mockRejectedValueOnce({ code: "internal", message: "db locked" })
      .mockResolvedValueOnce(SNIPPETS.map((s) => ({ ...s })));
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("db locked"));
    clickButton(mounted!.container, "重试");
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));
    expect(mocks.snippetList).toHaveBeenCalledTimes(2);
  });

  it("creates a snippet through the editor", async () => {
    mocks.snippetCreate.mockResolvedValue({ id: "sn9" });
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "新建片段"));
    const modal = mounted.container.querySelectorAll(".nx-modal")[1];
    const name = modal?.querySelectorAll("input.nx-input")[0];
    const body = modal?.querySelector("textarea");
    if (!name || !body) throw new Error("Snippet editor fields not found");
    setInputValue(name as HTMLInputElement, "看日志");
    setInputValue(body as HTMLTextAreaElement, "tail -f /var/log/app.log");
    clickButton(mounted.container, "保存");
    await waitFor(() =>
      expect(mocks.snippetCreate).toHaveBeenCalledWith("看日志", "tail -f /var/log/app.log"),
    );
  });

  it("creates a snippet with leading/trailing spaces and Tab preserved verbatim", async () => {
    mocks.snippetCreate.mockResolvedValue({ id: "sn9" });
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "新建片段"));
    const modal = mounted.container.querySelectorAll(".nx-modal")[1];
    const name = modal?.querySelectorAll("input.nx-input")[0];
    const body = modal?.querySelector("textarea");
    if (!name || !body) throw new Error("Snippet editor fields not found");
    setInputValue(name as HTMLInputElement, "带空白");
    // 首尾空格 / Tab 都可能有语义：保存路径一个字节都不能动（trim 只做空白校验）
    setInputValue(body as HTMLTextAreaElement, "  docker ps \t ");
    clickButton(mounted.container, "保存");
    await waitFor(() => expect(mocks.snippetCreate).toHaveBeenCalledTimes(1));
    expect(mocks.snippetCreate).toHaveBeenCalledWith("带空白", "  docker ps \t ");
  });

  it("edits a snippet keeping the raw body — trailing CR/LF is meaningful content", async () => {
    mocks.snippetList.mockResolvedValue([
      { id: "sn9", name: "带CRLF", body: "echo done\r\n", groupId: null, sort: 1 },
    ]);
    mocks.snippetUpdate.mockResolvedValue(undefined);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("带CRLF"));

    click(buttonByTitle(mounted.container, "编辑片段"));
    // 不动正文直接保存：状态里的原始 body（含 \r\n）必须原样传给 update，
    // 不能被 trim 成 "echo done"
    clickButton(mounted.container, "保存");
    await waitFor(() => expect(mocks.snippetUpdate).toHaveBeenCalledTimes(1));
    expect(mocks.snippetUpdate).toHaveBeenCalledWith("sn9", "带CRLF", "echo done\r\n");
  });

  it("refuses a whitespace-only body — trim is validation, not a write", async () => {
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "新建片段"));
    const modal = mounted.container.querySelectorAll(".nx-modal")[1];
    const name = modal?.querySelectorAll("input.nx-input")[0];
    const body = modal?.querySelector("textarea");
    if (!name || !body) throw new Error("Snippet editor fields not found");
    setInputValue(name as HTMLInputElement, "空白正文");
    setInputValue(body as HTMLTextAreaElement, " \t\r\n ");
    const save = [...mounted.container.querySelectorAll(".nx-modal")[1]!.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存",
    );
    if (!save) throw new Error("Save button not found");
    // 纯空白正文：保存按钮直接禁用，任何写都不会发生
    expect(save).toHaveProperty("disabled", true);
    click(save);
    await flush();
    expect(mocks.snippetCreate).not.toHaveBeenCalled();
    expect(mocks.snippetUpdate).not.toHaveBeenCalled();
  });

  it("a saved multi-line snippet inserts with one execute-risk confirm and original bytes", async () => {
    setTerminalWorkspace("kernel-1");
    const created = { id: "sn9", name: "两段", body: "echo a\necho b", groupId: null, sort: 3 };
    mocks.snippetCreate.mockResolvedValue({ id: "sn9" });
    mocks.snippetList
      .mockResolvedValueOnce(SNIPPETS.map((s) => ({ ...s })))
      .mockResolvedValue([...SNIPPETS.map((s) => ({ ...s })), created]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "新建片段"));
    const modal = mounted.container.querySelectorAll(".nx-modal")[1];
    const name = modal?.querySelectorAll("input.nx-input")[0];
    const body = modal?.querySelector("textarea");
    if (!name || !body) throw new Error("Snippet editor fields not found");
    setInputValue(name as HTMLInputElement, "两段");
    setInputValue(body as HTMLTextAreaElement, "echo a\necho b");
    clickButton(mounted.container, "保存");
    await waitFor(() => expect(mocks.snippetCreate).toHaveBeenCalledWith("两段", "echo a\necho b"));
    // 保存后列表刷新，新片段出现在面板里
    await waitFor(() => expect(mounted!.container.textContent).toContain("两段"));

    const rows = [...mounted.container.querySelectorAll(".nx-row")];
    const createdRow = rows.find((r) => r.textContent?.includes("两段"));
    if (!createdRow) throw new Error("Created snippet row not found");
    click(buttonByTitle(createdRow, "插入到当前终端"));
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    // 保存 → 插入串联：恰好一次 execute-risk 确认（文案如实说明逐行提交），写入原始字节
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("回车/换行"),
      expect.objectContaining({ kind: "warning" }),
    );
    const [tabId, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(tabId).toBe("kernel-1");
    expect(new TextDecoder().decode(bytes)).toBe("echo a\necho b");
  });

  it("edits a snippet without ever writing to the terminal", async () => {
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "编辑片段"));
    const modal = mounted.container.querySelectorAll(".nx-modal")[1];
    const body = modal?.querySelector("textarea");
    if (!body) throw new Error("Snippet body textarea not found");
    expect((body as HTMLTextAreaElement).value).toBe("docker ps");
    setInputValue(body as HTMLTextAreaElement, "docker ps -a");
    clickButton(mounted.container, "保存");
    await waitFor(() =>
      expect(mocks.snippetUpdate).toHaveBeenCalledWith("sn1", "看容器状态", "docker ps -a"),
    );
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("insert writes the body into the active terminal without executing it", async () => {
    setTerminalWorkspace("kernel-1");
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    // 可打印内容无执行风险：不需要确认
    expect(mocks.ask).not.toHaveBeenCalled();
    const [tabId, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(tabId).toBe("kernel-1");
    const text = new TextDecoder().decode(bytes);
    expect(text).toBe("docker ps");
    // 不带回车：写入 ≠ 执行，回车由用户自己按
    expect(text.endsWith("\n")).toBe(false);
    expect(text.endsWith("\r")).toBe(false);
  });

  it("asks before inserting a multi-line snippet (each line would execute)", async () => {
    setTerminalWorkspace("kernel-1");
    mocks.snippetList.mockResolvedValue([
      { id: "sn9", name: "两段", body: "echo a\necho b", groupId: null, sort: 1 },
    ]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("两段"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.write).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.write).toHaveBeenCalledTimes(1);
    // 第二次 ask 必须被真正消费（防止 once 配置被 reset 静默吞掉）
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    // 已确认的含换行插入，结果提示不得再绝对声称「未执行」
    const toastText = mocks.toast.mock.calls.at(-1)?.[1] as string;
    expect(toastText).not.toContain("未执行");
  });

  it("gates a CR-only snippet — bare \\r submits the line in a PTY", async () => {
    setTerminalWorkspace("kernel-1");
    mocks.snippetList.mockResolvedValue([
      { id: "sn9", name: "cr", body: "echo M59_R_EXECUTED\r#", groupId: null, sort: 1 },
    ]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("cr"));

    // 未授权前绝不写入
    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.write).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.write).toHaveBeenCalledTimes(1);
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    const [, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    // 内容字节原样保留（含中间的 0x0d），确认 ≠ 删改
    expect(new TextDecoder().decode(bytes)).toBe("echo M59_R_EXECUTED\r#");
    const toastText = mocks.toast.mock.calls.at(-1)?.[1] as string;
    expect(toastText).not.toContain("未执行");
  });

  it("gates a snippet with an embedded CR mid-body", async () => {
    setTerminalWorkspace("kernel-1");
    mocks.snippetList.mockResolvedValue([
      { id: "sn9", name: "内嵌CR", body: "echo a\recho b", groupId: null, sort: 1 },
    ]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("内嵌CR"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.write).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.write).toHaveBeenCalledTimes(1);
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    const [, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(new TextDecoder().decode(bytes)).toBe("echo a\recho b");
  });

  it("gates a CRLF snippet like any other line submission", async () => {
    setTerminalWorkspace("kernel-1");
    mocks.snippetList.mockResolvedValue([
      { id: "sn9", name: "CRLF", body: "echo a\r\necho b", groupId: null, sort: 1 },
    ]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("CRLF"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.write).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.write).toHaveBeenCalledTimes(1);
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    const [, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(new TextDecoder().decode(bytes)).toBe("echo a\r\necho b");
  });

  it("gates terminal control characters that can change state without executing", async () => {
    setTerminalWorkspace("kernel-1");
    mocks.snippetList.mockResolvedValue([
      { id: "sn9", name: "ctrl", body: "echo a\u0003", groupId: null, sort: 1 },
    ]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("ctrl"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.write).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.write).toHaveBeenCalledTimes(1);
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    const [, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(new TextDecoder().decode(bytes)).toBe("echo a\u0003");
    const toastText = mocks.toast.mock.calls.at(-1)?.[1] as string;
    expect(toastText).not.toContain("未执行");
  });

  it("gates Tab — programmable completion runs shell code without Enter", async () => {
    setTerminalWorkspace("kernel-1");
    mocks.snippetList.mockResolvedValue([
      { id: "sn9", name: "tab", body: "foo \t", groupId: null, sort: 1 },
    ]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("tab"));

    // 未授权前绝不写入
    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.write).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.write).toHaveBeenCalledTimes(1);
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    const [, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(new TextDecoder().decode(bytes)).toBe("foo \t");
    const toastText = mocks.toast.mock.calls.at(-1)?.[1] as string;
    expect(toastText).not.toContain("未执行");
  });

  it.each([
    ["DEL", "ab\u007f"],
    ["NUL", "a\u0000b"],
    ["ESC", "a\u001bb"],
    ["C1 NEL", "a\u0085b"],
  ])("gates %s as a terminal-state control character", async (_label, body) => {
    setTerminalWorkspace("kernel-1");
    mocks.snippetList.mockResolvedValue([
      { id: "sn9", name: "边界", body, groupId: null, sort: 1 },
    ]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("边界"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.write).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.write).toHaveBeenCalledTimes(1);
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    const [, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(new TextDecoder().decode(bytes)).toBe(body);
  });

  it("does not gate truly printable content (space, ~, CJK)", async () => {
    setTerminalWorkspace("kernel-1");
    mocks.snippetList.mockResolvedValue([
      { id: "sn9", name: "可打印", body: "df -h ~ /中文", groupId: null, sort: 1 },
    ]);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("可打印"));

    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    expect(mocks.ask).not.toHaveBeenCalled();
    const [, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(new TextDecoder().decode(bytes)).toBe("df -h ~ /中文");
  });

  it("refuses to insert with a clear toast when no terminal is active", async () => {
    setTerminalWorkspace(null);
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    click(buttonByTitle(mounted.container, "插入到当前终端"));
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("info", expect.stringContaining("终端"));
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("deletes a snippet only after the confirmation resolves true", async () => {
    mounted = mountWithClient(createElement(SnippetsPanel, { onClose: vi.fn() }));
    await waitFor(() => expect(mounted!.container.textContent).toContain("看容器状态"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted.container, "删除片段"));
    await flush();
    expect(mocks.snippetDelete).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted.container, "删除片段"));
    await waitFor(() => expect(mocks.snippetDelete).toHaveBeenCalledWith("sn1"));
    expect(mocks.ask).toHaveBeenCalledTimes(2);
  });
});

// ───────── R42 审计钉板：资产删除保持 info ─────────
//
// Grid 报告把 AssetTree 的删除列进了候选，逐项审计后的结论是**不动**：
// 那是软删除（文案自述「可恢复；关联凭据保留」），不是 genuinely destructive。
// 这条回归把「保持默认 info 级别」钉住，防止后续无差别补 warning 把它误标。

describe("asset delete confirmation (R42 audit pin)", () => {
  it("asset delete stays info-level: soft delete is recoverable", async () => {
    mocks.list.mockResolvedValue([
      {
        id: "a1",
        groupId: null,
        kind: "ssh",
        name: "web-1",
        host: "10.0.0.8",
        port: 22,
        username: "root",
        authKind: "password",
        keyPath: null,
        credId: null,
        options: {},
        tags: "",
        note: "",
        sort: 0,
        createdAt: 1,
        updatedAt: 1,
        deletedAt: null,
        builtin: false,
      },
    ]);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    mocks.ask.mockResolvedValueOnce(false);
    click(buttonByTitle(mounted!.container, "删除"));
    await flush();
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringContaining("软删除，可恢复"));
    // 钉住审计结论：单参数调用（没有 { kind: "warning" }）
    expect(mocks.ask.mock.calls[0]).toHaveLength(1);
    expect(mocks.assetDelete).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    click(buttonByTitle(mounted!.container, "删除"));
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledWith("a1"));
  });
});
