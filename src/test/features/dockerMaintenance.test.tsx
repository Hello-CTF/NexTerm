/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushMacrotask, mount, setInputValue, waitFor, type MountedView } from "./reactTestUtils";
import type { ComposeStack, ContainerSummary, DockerPrunePreview } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({
  composeAction: vi.fn(),
  composePrepare: vi.fn(),
  composeAttach: vi.fn(),
  fsWrite: vi.fn(),
  diskUsage: vi.fn(),
  prunePreview: vi.fn(),
  prune: vi.fn(),
  closeTab: vi.fn(),
  ask: vi.fn(),
  saveFile: null as null | ((change: { sessionId: string; path: string; previous: string; baseContentBase64: string; content: string }) => Promise<boolean>),
  saveCalled: 0,
  editorChange: {
    sessionId: "s1",
    path: "/srv/shop/compose.yml",
    previous: "services: old\n",
    baseContentBase64: "c2VydmljZXM6IG9sZAo=",
    content: "services: new\n",
  },
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: {
      ...actual.dockerApi,
      composeAction: mocks.composeAction,
      composeRedeployPrepare: mocks.composePrepare,
      composeRedeployAttach: mocks.composeAttach,
      diskUsage: mocks.diskUsage,
      prunePreview: mocks.prunePreview,
      prune: mocks.prune,
    },
    fsApi: { ...actual.fsApi, write: mocks.fsWrite },
    terminalApi: { ...actual.terminalApi, closeTab: mocks.closeTab },
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    createBinaryChannel: vi.fn(() => ({ id: "channel-1" })),
    disposeChannel: vi.fn(),
  };
});

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

vi.mock("../../features/files/FileEditor", async () => {
  const { createElement } = await import("react");
  return {
    FileEditor: (props: { saveFile: NonNullable<typeof mocks.saveFile> }) => {
      mocks.saveFile = props.saveFile;
      return createElement("button", {
        type: "button",
        onClick: () => {
          mocks.saveCalled++;
          void props.saveFile(mocks.editorChange);
        },
      }, "trigger-save");
    },
  };
});

import { ComposeStackHeader } from "../../features/docker/ComposeStackHeader";
import { ComposeEditor } from "../../features/docker/ComposeEditor";
import { DockerDiskPanel } from "../../features/docker/DockerDiskPanel";
import { groupContainersByCompose } from "../../features/docker/composeGrouping";
import { dockerTextDiff } from "../../features/docker/textDiff";
import { useUi } from "../../app/store";

const stack: ComposeStack = {
  project: "shop",
  workingDir: "/srv/shop",
  configFiles: ["/srv/shop/compose.yml"],
  envPath: "/srv/shop/.env",
  envExists: true,
  services: ["web"],
  readOnly: false,
};

function challenge() {
  return {
    code: "needs_confirm",
    detail: {
      kind: "docker_production_confirmation",
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
      productionTargets: [{ sessionId: "s1", sessionName: "production", environment: "production" }],
    },
  };
}

// The current M15 docker challenge: it always carries the environment names a
// confirmation must type back.
function typedChallenge() {
  return {
    code: "needs_confirm",
    detail: {
      kind: "docker_production_confirmation",
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
      requiredEnvironmentNames: ["production"],
      productionTargets: [{ sessionId: "s1", sessionName: "production", environment: "production" }],
    },
  };
}

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

async function typeEnvironmentNameAndConfirm() {
  const input = document.body.querySelector<HTMLInputElement>('input[aria-label="输入环境名 production"]');
  if (!input) throw new Error("environment input not found");
  const confirm = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("确认执行"));
  if (!confirm) throw new Error("production confirm not found");
  expect(confirm.disabled).toBe(true);
  setInputValue(input, "production");
  expect(confirm.disabled).toBe(false);
  // The retry continues asynchronously; keep an act scope open across a
  // macrotask so its state updates land inside act.
  await act(async () => {
    confirm.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flushMacrotask();
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: vi.fn() });
  mocks.saveFile = null;
  mocks.saveCalled = 0;
  mocks.editorChange = {
    sessionId: "s1",
    path: "/srv/shop/compose.yml",
    previous: "services: old\n",
    baseContentBase64: "c2VydmljZXM6IG9sZAo=",
    content: "services: new\n",
  };
  mocks.ask.mockResolvedValue(true);
  mocks.fsWrite.mockResolvedValue(undefined);
  mocks.closeTab.mockResolvedValue(undefined);
  mocks.composePrepare.mockResolvedValue({ ticket: "ticket-1", contentHash: "content-hash-1" });
  mocks.composeAttach.mockResolvedValue("up-stream");
});

describe("Compose stack grouping and production actions", () => {
  it("groups by project and keeps ungrouped containers last", () => {
    const container = (id: string, project: string | null): ContainerSummary => ({
      id, name: id, image: "example", state: "running", status: "Up", ports: "", composeProject: project,
    });
    const groups = groupContainersByCompose(
      [container("b1", "beta"), container("plain", null), container("a1", "alpha")],
      [{ ...stack, project: "alpha" }],
    );
    expect(groups.map((group) => group.project)).toEqual(["alpha", "beta", null]);
    expect(groups[0].stack?.project).toBe("alpha");
    expect(groups[1].stack).toBeNull();
    expect(groups[2].containers[0].id).toBe("plain");
  });

  it("rejects a down challenge without environment names and does not retry", async () => {
    mocks.composeAction.mockRejectedValueOnce(challenge());
    const mounted = mountWithClient(createElement(ComposeStackHeader, { sessionId: "s1", project: "shop", stack, onChanged: vi.fn() }));
    clickButton(mounted.container, "down");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mocks.composeAction).toHaveBeenCalledTimes(1));
    await flush();

    expect(document.body.querySelector('input[aria-label="输入生产环境确认文字"]')).toBeNull();
    expect(document.body.querySelector('input[aria-label="输入环境名 production"]')).toBeNull();
    expect(mocks.composeAction).toHaveBeenCalledTimes(1);
    mounted.unmount();
  });

  it("retries the M15 typed challenge before down with the typed environment names", async () => {
    mocks.composeAction.mockRejectedValueOnce(typedChallenge()).mockResolvedValueOnce("down done");
    const mounted = mountWithClient(createElement(ComposeStackHeader, { sessionId: "s1", project: "shop", stack, onChanged: vi.fn() }));
    clickButton(mounted.container, "down");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mocks.composeAction).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(document.body.querySelector('input[aria-label="输入环境名 production"]')).not.toBeNull());
    await typeEnvironmentNameAndConfirm();
    await waitFor(() => expect(mocks.composeAction).toHaveBeenCalledTimes(2));
    expect(mocks.composeAction.mock.calls[1][3]).toEqual({
      confirmed: true,
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
      environmentNames: ["production"],
    });
    await waitFor(() => expect(mounted.container.textContent).toContain("down done"));
    mounted.unmount();
  });
});

describe("Compose edit and redeploy", () => {
  it("shows diff and completes production guard before writing and streaming up", async () => {
    mocks.composePrepare.mockRejectedValueOnce(typedChallenge());
    const mounted = mountWithClient(createElement(ComposeEditor, { sessionId: "s1", stack, onClose: vi.fn(), onChanged: vi.fn() }));
    clickButton(document.body, "trigger-save");
    await waitFor(() => expect(mocks.saveCalled).toBe(1));
    await waitFor(() => expect(document.body.textContent).toContain("保存并重新部署"));
    expect(document.body.textContent).toContain("services: old");
    expect(document.body.textContent).toContain("services: new");
    expect(mocks.composePrepare).not.toHaveBeenCalled();
    expect(mocks.fsWrite).not.toHaveBeenCalled();

    clickButton(document.body, "保存并 up -d");
    await waitFor(() => expect(mocks.composePrepare).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(document.body.querySelector('input[aria-label="输入环境名 production"]')).not.toBeNull());
    expect(mocks.fsWrite).not.toHaveBeenCalled();
    expect(mocks.composeAttach).not.toHaveBeenCalled();
    await typeEnvironmentNameAndConfirm();
    await waitFor(() => expect(mocks.composePrepare).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mocks.composeAttach).toHaveBeenCalledTimes(1));
    expect(mocks.fsWrite).not.toHaveBeenCalled();
    expect(mocks.composePrepare.mock.calls[1][0]).toMatchObject({
      sessionId: "s1",
      project: "shop",
      path: "/srv/shop/compose.yml",
      baseContentBase64: mocks.editorChange.baseContentBase64,
      contentBase64: "c2VydmljZXM6IG5ldwo=",
      productionConfirmation: {
        confirmed: true,
        targetSetHash: "a".repeat(64),
        commandHash: "b".repeat(64),
        environmentNames: ["production"],
      },
    });
    expect(mocks.composeAttach.mock.calls[0][0]).toMatchObject({
      sessionId: "s1",
      project: "shop",
      path: "/srv/shop/compose.yml",
      ticket: "ticket-1",
    });
    expect(mocks.composeAttach.mock.calls[0][0]).not.toHaveProperty("productionConfirmation");
    expect(mocks.composePrepare.mock.invocationCallOrder[1]).toBeLessThan(mocks.composeAttach.mock.invocationCallOrder[0]);
    await waitFor(() => expect(document.body.textContent).toContain("shop · up -d"));
    mounted.unmount();
  });

  it("retries the M15 typed challenge on redeploy with the typed environment names", async () => {
    mocks.composePrepare.mockRejectedValueOnce(typedChallenge());
    const mounted = mountWithClient(createElement(ComposeEditor, { sessionId: "s1", stack, onClose: vi.fn(), onChanged: vi.fn() }));
    clickButton(document.body, "trigger-save");
    await waitFor(() => expect(mocks.saveCalled).toBe(1));
    await waitFor(() => expect(document.body.textContent).toContain("保存并重新部署"));

    clickButton(document.body, "保存并 up -d");
    await waitFor(() => expect(mocks.composePrepare).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(document.body.querySelector('input[aria-label="输入环境名 production"]')).not.toBeNull());
    await typeEnvironmentNameAndConfirm();
    await waitFor(() => expect(mocks.composePrepare).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mocks.composeAttach).toHaveBeenCalledTimes(1));
    expect(mocks.composePrepare.mock.calls[1][0]).toMatchObject({
      sessionId: "s1",
      project: "shop",
      path: "/srv/shop/compose.yml",
      productionConfirmation: {
        confirmed: true,
        targetSetHash: "a".repeat(64),
        commandHash: "b".repeat(64),
        environmentNames: ["production"],
      },
    });
    mounted.unmount();
  });

  it("requires re-entry when a consecutive challenge arrives with a new hash", async () => {
    const secondChallenge = {
      code: "needs_confirm",
      detail: {
        kind: "docker_production_confirmation",
        targetSetHash: "c".repeat(64),
        commandHash: "d".repeat(64),
        requiredEnvironmentNames: ["production"],
        productionTargets: [{ sessionId: "s1", sessionName: "production", environment: "production" }],
      },
    };
    mocks.composePrepare
      .mockRejectedValueOnce(typedChallenge())
      .mockRejectedValueOnce(secondChallenge)
      .mockResolvedValue({ ticket: "ticket-1", contentHash: "content-hash-1" });
    const mounted = mountWithClient(createElement(ComposeEditor, { sessionId: "s1", stack, onClose: vi.fn(), onChanged: vi.fn() }));
    clickButton(document.body, "trigger-save");
    await waitFor(() => expect(document.body.textContent).toContain("保存并重新部署"));
    clickButton(document.body, "保存并 up -d");
    await waitFor(() => expect(mocks.composePrepare).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(document.body.querySelector('input[aria-label="输入环境名 production"]')).not.toBeNull());

    // 第一个挑战：输入并确认
    await typeEnvironmentNameAndConfirm();
    await waitFor(() => expect(mocks.composePrepare).toHaveBeenCalledTimes(2));

    // 同 target/environment、不同 hash 的连续挑战必须清空并锁定：上一挑战的
    // 输入不得用于新身份。
    await waitFor(() => expect(document.body.querySelector('input[aria-label="输入环境名 production"]')).not.toBeNull());
    const input = document.body.querySelector<HTMLInputElement>('input[aria-label="输入环境名 production"]');
    expect(input?.value).toBe("");
    const confirm = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.includes("确认执行"),
    );
    expect(confirm?.disabled).toBe(true);

    // 针对新身份重新输入后，确认携带新 hash 与实输 names
    await typeEnvironmentNameAndConfirm();
    await waitFor(() => expect(mocks.composePrepare).toHaveBeenCalledTimes(3));
    expect(mocks.composePrepare.mock.calls[2][0]).toMatchObject({
      productionConfirmation: {
        confirmed: true,
        targetSetHash: "c".repeat(64),
        commandHash: "d".repeat(64),
        environmentNames: ["production"],
      },
    });
    mounted.unmount();
  });

  it("redacts old and new .env values from the diff DOM", async () => {
    mocks.editorChange = {
      sessionId: "s1",
      path: stack.envPath,
      previous: "PASSWORD=old-secret\nAPI_TOKEN=old-token\n",
      baseContentBase64: "b2xk",
      content: "PASSWORD=new-secret\nAPI_TOKEN=new-token\n",
    };
    const mounted = mountWithClient(createElement(ComposeEditor, { sessionId: "s1", stack, onClose: vi.fn(), onChanged: vi.fn() }));
    clickButton(document.body, "trigger-save");
    await waitFor(() => expect(document.body.textContent).toContain("值已脱敏"));
    const text = document.body.textContent ?? "";
    expect(text).toContain("PASSWORD");
    expect(text).toContain("API_TOKEN");
    expect(text).toContain("值已脱敏");
    expect(text).not.toContain("old-secret");
    expect(text).not.toContain("new-secret");
    expect(text).not.toContain("old-token");
    expect(text).not.toContain("new-token");
    mounted.unmount();
  });

  it("detects an EOF newline change", () => {
    expect(dockerTextDiff("a", "a")).toEqual([]);
    expect(dockerTextDiff("a", "a\n")).toContainEqual({ kind: "add", text: "" });
    expect(dockerTextDiff("a\n", "a")).toContainEqual({ kind: "del", text: "" });
  });
});

describe("Docker disk prune", () => {
  it("previews exact items and rejects a challenge without environment names without retrying", async () => {
    const preview: DockerPrunePreview = {
      token: "preview-token",
      digest: "c".repeat(64),
      expiresAt: Date.now() + 60_000,
      items: [{ kind: "containers", id: "c1", name: "old-web", bytes: 2048 }],
      totalBytes: 2048,
    };
    mocks.diskUsage.mockResolvedValue({
      categories: [
        { kind: "images", total: 1, active: 1, sizeBytes: 1000, reclaimableBytes: 0 },
        { kind: "containers", total: 1, active: 0, sizeBytes: 2048, reclaimableBytes: 2048 },
        { kind: "volumes", total: 1, active: 0, sizeBytes: 1000, reclaimableBytes: 1000 },
        { kind: "buildCache", total: 0, active: 0, sizeBytes: 0, reclaimableBytes: 0 },
      ],
      images: [], containers: [],
      volumes: [{ name: "orphan", driver: "local", links: 0, sizeBytes: 1000, containers: [] }],
      networks: [{ id: "n1", name: "empty", driver: "bridge", scope: "local", builtin: false, containers: [] }],
      buildCache: [],
    });
    mocks.prunePreview.mockResolvedValue(preview);
    mocks.prune.mockRejectedValueOnce(challenge());
    const mounted = mountWithClient(createElement(DockerDiskPanel, { sessionId: "s1", visible: true }));
    await waitFor(() => expect(mounted.container.textContent).toContain("orphan"));
    clickButton(mounted.container, "生成精确预览");
    await waitFor(() => expect(mocks.prunePreview).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mounted.container.textContent).toContain("将删除 1 项"));
    expect(mounted.container.textContent).toContain("old-web");
    expect(mocks.prune).not.toHaveBeenCalled();
    await waitFor(() => {
      const button = [...mounted.container.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === "确认删除预览项");
      expect(button?.disabled).toBe(false);
    });
    clickButton(mounted.container, "确认删除预览项");
    await waitFor(() => expect(mocks.prune).toHaveBeenCalledTimes(1));
    await flush();

    expect(mounted.container.textContent).toContain("prune 执行失败");
    expect(mocks.prune).toHaveBeenCalledTimes(1);
    expect(mocks.prune).toHaveBeenCalledWith("s1", "preview-token", "c".repeat(64), undefined);
    expect(document.body.querySelector('input[aria-label="输入生产环境确认文字"]')).toBeNull();
    expect(document.body.querySelector('input[aria-label="输入环境名 production"]')).toBeNull();
    expect(mounted.container.textContent).not.toContain("成功项释放");
    mounted.unmount();
  });

  it("retries the M15 typed challenge on prune with the typed environment names", async () => {
    const preview: DockerPrunePreview = {
      token: "preview-token",
      digest: "c".repeat(64),
      expiresAt: Date.now() + 60_000,
      items: [{ kind: "containers", id: "c1", name: "old-web", bytes: 2048 }],
      totalBytes: 2048,
    };
    mocks.diskUsage.mockResolvedValue({
      categories: [
        { kind: "images", total: 1, active: 1, sizeBytes: 1000, reclaimableBytes: 0 },
        { kind: "containers", total: 1, active: 0, sizeBytes: 2048, reclaimableBytes: 2048 },
        { kind: "volumes", total: 1, active: 0, sizeBytes: 1000, reclaimableBytes: 1000 },
        { kind: "buildCache", total: 0, active: 0, sizeBytes: 0, reclaimableBytes: 0 },
      ],
      images: [], containers: [],
      volumes: [{ name: "orphan", driver: "local", links: 0, sizeBytes: 1000, containers: [] }],
      networks: [{ id: "n1", name: "empty", driver: "bridge", scope: "local", builtin: false, containers: [] }],
      buildCache: [],
    });
    mocks.prunePreview.mockResolvedValue(preview);
    mocks.prune.mockRejectedValueOnce(typedChallenge()).mockResolvedValueOnce({
      items: [{ item: preview.items[0], output: "removed" }], freedBytes: 2048,
    });
    const mounted = mountWithClient(createElement(DockerDiskPanel, { sessionId: "s1", visible: true }));
    await waitFor(() => expect(mounted.container.textContent).toContain("orphan"));
    clickButton(mounted.container, "生成精确预览");
    await waitFor(() => expect(mocks.prunePreview).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mounted.container.textContent).toContain("将删除 1 项"));
    clickButton(mounted.container, "确认删除预览项");
    await waitFor(() => expect(mocks.prune).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(document.body.querySelector('input[aria-label="输入环境名 production"]')).not.toBeNull());
    await typeEnvironmentNameAndConfirm();
    await waitFor(() => expect(mocks.prune).toHaveBeenCalledTimes(2));
    expect(mocks.prune.mock.calls[1]).toEqual([
      "s1", "preview-token", "c".repeat(64),
      { confirmed: true, targetSetHash: "a".repeat(64), commandHash: "b".repeat(64), environmentNames: ["production"] },
    ]);
    await waitFor(() => expect(mounted.container.textContent).toContain("成功项释放"));
    mounted.unmount();
  });
});
