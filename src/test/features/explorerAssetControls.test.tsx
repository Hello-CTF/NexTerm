/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  deferred,
  flush,
  mount,
  setInputValue,
  setSelectValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  update: vi.fn(),
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
  realAsk: null as null | ((message: string, options?: { title?: string; kind?: "info" | "warning" | "error" }) => Promise<boolean>),
  pickKeyFile: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  mocks.realAsk = actual.ask;
  return { ...actual, ask: mocks.ask, pickKeyFile: mocks.pickKeyFile };
});
vi.mock("../../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webFiles")>();
  return {
    ...actual,
    browserFilesAvailable: () => false,
    pickBrowserFile: vi.fn(),
  };
});
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      list: mocks.list,
      search: vi.fn(),
      update: mocks.update,
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
  };
});

import { AssetTree, AssetEditor } from "../../features/explorer/AssetTree";
import { SnippetsPanel } from "../../features/explorer/SnippetsPanel";
import { useUi } from "../../app/store";
import { dialogLevelForKind } from "../../app/App";
import { registerDialogHandlers } from "../../ui/dialogs";
import { DialogHost } from "../../ui/DialogHost";

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

describe("asset group controls", () => {
  it("renames only through the explicit dialog — never on row selection", async () => {
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产"));

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
    expect(mounted!.container.textContent).toContain("删除失败：");
    expect(mounted!.container.textContent).toContain("disk on fire");
    expect(mocks.ask).toHaveBeenCalledTimes(1);

    clickButton(mounted!.container, "重试");
    await waitFor(() => expect(mocks.groupDelete).toHaveBeenCalledTimes(2));
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    await flush();
    expect(mounted!.container.textContent).not.toContain("disk on fire");
  });

  it("moves an asset out of a group with a clear toast", async () => {
    mocks.list.mockResolvedValue([
      {
        id: "a1",
        groupId: "g1",
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
    mocks.update.mockResolvedValue(undefined);
    mounted = mountWithClient(createElement(AssetTree));
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    const tree = mounted!.container.querySelector("[role='tree']");
    if (!tree) throw new Error("Tree not found");
    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", {
      value: { types: ["application/x-nexterm-asset"], getData: () => "a1" },
    });
    act(() => {
      tree.dispatchEvent(drop);
    });
    await waitFor(() => expect(mocks.update).toHaveBeenCalledWith({ id: "a1", groupId: null }));
    expect(mocks.toast).toHaveBeenCalledWith("info", "已移出分组");
  });
});

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
    const password = [
      ...mounted!.container.querySelectorAll<HTMLInputElement>('input[type="password"]'),
    ][0];
    if (password) setInputValue(password, "hunter2");

    mocks.probe.mockResolvedValue({ open: true });
    clickButton(mounted!.container, "测试端口");
    await waitFor(() => expect(mocks.probe).toHaveBeenCalledTimes(1));
    expect(mocks.probe).toHaveBeenCalledWith("10.0.0.8", 22, 3000);
    expect(mocks.probe.mock.calls[0]).toHaveLength(3);
    await flush();
    expect(mounted!.container.textContent).toContain("端口可达");
  });

  it("does not double-submit while a probe is in flight", async () => {
    await mountEditor();
    await setHost("10.0.0.8");

    const pending = deferred<{ open: boolean }>();
    mocks.probe.mockReturnValue(pending.promise);
    clickButton(mounted!.container, "测试端口");
    clickButton(mounted!.container, "测试端口");
    clickButton(mounted!.container, "测试端口");
    await flush();
    expect(mocks.probe).toHaveBeenCalledTimes(1);

    pending.resolve({ open: true });
    await flush();
    expect(mounted!.container.textContent).toContain("端口可达");
  });

  it("discards a stale result when the target changes mid-flight", async () => {
    await mountEditor();
    await setHost("10.0.0.8");

    const pending = deferred<{ open: boolean }>();
    mocks.probe.mockReturnValue(pending.promise);
    clickButton(mounted!.container, "测试端口");
    await flush();

    await setHost("10.0.0.9");
    pending.resolve({ open: true });
    await flush();
    expect(mounted!.container.textContent).not.toContain("端口可达");
  });

  it("surfaces a closed port with the kernel error text", async () => {
    await mountEditor();
    await setHost("10.0.0.8");

    mocks.probe.mockResolvedValue({ open: false, error: "connection refused" });
    clickButton(mounted!.container, "测试端口");
    await waitFor(() => expect(mocks.probe).toHaveBeenCalledTimes(1));
    await flush();
    expect(mounted!.container.textContent).toContain("端口不可达：connection refused");
  });
});

describe("asset editor shared credential hint", () => {
  const PASSWORD_CRED = {
    id: "c1",
    name: "生产密码",
    kind: "password",
    createdAt: 1,
    updatedAt: 1,
    usedBy: [{ id: "a9", name: "db-1", kind: "mysql" }],
    source: "inline",
    refPath: null,
    hasPassphrase: false,
  };
  const KEY_CRED = {
    id: "k1",
    name: "部署密钥",
    kind: "private_key",
    createdAt: 1,
    updatedAt: 1,
    usedBy: [{ id: "a8", name: "web-1", kind: "ssh" }],
    source: "inline",
    refPath: null,
    hasPassphrase: false,
  };

  function selectByOptionText(text: string): HTMLSelectElement {
    const select = [...mounted!.container.querySelectorAll("select")].find((s) =>
      [...s.options].some((o) => o.textContent?.includes(text)),
    );
    if (!select) throw new Error(`Select not found by option text: ${text}`);
    return select;
  }

  it("密码路径：选已有凭据时提示共用后果", async () => {
    mocks.listCredentials.mockResolvedValue([PASSWORD_CRED]);
    await mountEditor();
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产密码"));

    setSelectValue(selectByOptionText("生产密码"), "c1");
    await flush();
    expect(mounted!.container.textContent).toContain("↳ 这条凭据可给多个资产共用；修改后，所有使用它的资产都会生效。");
  });

  it("密码路径：未使用的凭据，提示不预设已有多个资产共用", async () => {
    mocks.listCredentials.mockResolvedValue([{ ...PASSWORD_CRED, usedBy: [] }]);
    await mountEditor();
    await waitFor(() => expect(mounted!.container.textContent).toContain("生产密码"));

    setSelectValue(selectByOptionText("生产密码"), "c1");
    await flush();
    expect(mounted!.container.textContent).toContain("↳ 这条凭据可给多个资产共用；修改后，所有使用它的资产都会生效。");
    expect(mounted!.container.textContent).not.toContain("被多个资产共用");
  });

  it("私钥路径：选已有私钥凭据时同一句提示", async () => {
    mocks.listCredentials.mockResolvedValue([KEY_CRED]);
    await mountEditor();

    setSelectValue(selectByOptionText("私钥文件"), "key");
    await flush();
    clickButton(mounted!.container, "存入凭据库");
    await flush();
    setSelectValue(selectByOptionText("选已有私钥凭据"), "existing");
    await waitFor(() => expect(mounted!.container.textContent).toContain("部署密钥"));

    setSelectValue(selectByOptionText("部署密钥"), "k1");
    await flush();
    expect(mounted!.container.textContent).toContain("↳ 这条凭据可给多个资产共用；修改后，所有使用它的资产都会生效。");
  });
});

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
    expect(mounted!.container.textContent).toContain("加载失败：");
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
    await waitFor(() => expect(mounted!.container.textContent).toContain("两段"));

    const rows = [...mounted.container.querySelectorAll(".nx-row")];
    const createdRow = rows.find((r) => r.textContent?.includes("两段"));
    if (!createdRow) throw new Error("Created snippet row not found");
    click(buttonByTitle(createdRow, "插入到当前终端"));
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
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
    expect(mocks.ask).not.toHaveBeenCalled();
    const [tabId, bytes] = mocks.write.mock.calls[0] as [string, Uint8Array];
    expect(tabId).toBe("kernel-1");
    const text = new TextDecoder().decode(bytes);
    expect(text).toBe("docker ps");
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
    expect(mocks.ask).toHaveBeenCalledTimes(2);
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

describe("asset delete confirmation (R42 audit pin, real DialogHost)", () => {
  it("asset delete renders a warning alertdialog: soft delete is recoverable", async () => {
    mocks.ask.mockImplementation((message: string, options?: { title?: string; kind?: "info" | "warning" | "error" }) =>
      mocks.realAsk!(message, options),
    );
    registerDialogHandlers({
      ask: (message, options) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({
            kind: "ask",
            message,
            title: options?.title,
            level: dialogLevelForKind(options?.kind),
            resolve,
          });
        }),
      confirm: (message) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({ kind: "confirm", message, level: "warning", resolve });
        }),
      message: (message) =>
        new Promise<void>((resolve) => {
          useUi
            .getState()
            .openAppDialog({ kind: "message", message, level: "info", resolve: () => resolve() });
        }),
      choose: vi.fn(),
    });
    useUi.setState({ appDialog: null });
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
    mounted = mountWithClient(
      createElement(
        "div",
        null,
        createElement(AssetTree),
        createElement(DialogHost),
      ),
    );
    await waitFor(() => expect(mounted!.container.textContent).toContain("web-1"));

    async function openModal(): Promise<HTMLElement> {
      await waitFor(() =>
        expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull(),
      );
      return mounted!.container.querySelector<HTMLElement>(".nx-modal")!;
    }
    async function closeModal(modal: HTMLElement, button: "取消" | "确定"): Promise<void> {
      clickButton(modal, button);
      await waitFor(() =>
        expect(mounted!.container.querySelector(".nx-modal")).toBeNull(),
      );
    }

    click(buttonByTitle(mounted!.container, "删除"));
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("关联的凭据会保留");
    await closeModal(modal, "取消");
    expect(mocks.assetDelete).not.toHaveBeenCalled();

    click(buttonByTitle(mounted!.container, "删除"));
    const modal2 = await openModal();
    expect(modal2.getAttribute("role")).toBe("alertdialog");
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.assetDelete).toHaveBeenCalledWith("a1"));
  });
});
