/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  update: vi.fn(),
  groupList: vi.fn(),
  listAssets: vi.fn(),
  listCredentials: vi.fn(),
  profiles: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      create: vi.fn(),
      update: mocks.update,
      groupList: mocks.groupList,
      groupCreate: vi.fn(),
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      list: mocks.listAssets,
      readKeyFile: vi.fn(),
      auditQuery: vi.fn(),
    },
    vaultApi: { listCredentials: mocks.listCredentials, setCredential: vi.fn() },
    sessionApi: { probe: vi.fn() },
    terminalApi: {},
    localShellApi: { profiles: mocks.profiles },
  };
});

import { AssetEditor } from "../features/explorer/AssetTree";
import { formatShellArgs, parseShellArgs, shellArgsError } from "../features/explorer/shellArgs";
import type { Asset } from "../ipc/commands";

const WSL = "C:\\Windows\\System32\\wsl.exe";
const GIT_BASH = "C:\\Program Files\\Git\\bin\\bash.exe";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listAssets.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.update.mockResolvedValue({ id: "local-1" });
  mocks.profiles.mockResolvedValue([
    { id: "pwsh", name: "PowerShell", path: "C:\\Program Files\\PowerShell\\7\\pwsh.exe", args: [] },
    { id: "wsl:Ubuntu", name: "WSL: Ubuntu", path: WSL, args: ["-d", "Ubuntu"] },
    { id: "git-bash", name: "Git Bash", path: GIT_BASH, args: ["--login", "-i"] },
  ]);
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountLocalEditor(options: Record<string, unknown>) {
  const initial = { id: "local-1", kind: "local", name: "wsl", tags: "", note: "", options } as unknown as Asset;
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(AssetEditor, { kind: "asset", initial, onClose: vi.fn(), onSaved: vi.fn() }),
    ),
  );
}

function input(label: string): HTMLInputElement {
  const element = mounted!.container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
  if (!element) throw new Error(`${label} input not found`);
  return element;
}

function saveButton(): HTMLButtonElement {
  const button = [...mounted!.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === "保存");
  if (!button) throw new Error("save button not found");
  return button as HTMLButtonElement;
}

describe("shell start arguments", () => {
  it("round-trips quoted arguments and Windows paths", () => {
    expect(parseShellArgs(`-d "My Distro" --cd 'C:\\Users\\a b'  -i`)).toEqual(["-d", "My Distro", "--cd", "C:\\Users\\a b", "-i"]);
    expect(parseShellArgs("  ")).toEqual([]);
    expect(parseShellArgs(`-c ""`)).toEqual(["-c", ""]);
    const args = ["-d", "My Distro", `say "hi"`, ""];
    expect(parseShellArgs(formatShellArgs(args))).toEqual(args);
  });

  it("reports the backend limits", () => {
    expect(shellArgsError(["-d", "Ubuntu"])).toBeNull();
    expect(shellArgsError(Array.from({ length: 33 }, () => "-x"))).not.toBeNull();
    expect(shellArgsError(["a".repeat(1025)])).not.toBeNull();
    expect(shellArgsError(["a\u0007"])).not.toBeNull();
  });
});

describe("local asset shell picker", () => {
  it("shows the stored profile and saves the arguments of a newly picked one", async () => {
    mountLocalEditor({ shell: WSL, shellArgs: ["-d", "Ubuntu"] });
    await flush();
    await flush();
    const picker = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="选择本机 Shell"]');
    expect(picker?.textContent).toContain("WSL: Ubuntu");
    expect(input("Shell 启动参数").value).toBe("-d Ubuntu");

    click(picker!);
    await flush();
    const option = [...document.querySelectorAll('[role="option"]')].find((item) => item.textContent?.includes("Git Bash"));
    click(option!);
    await flush();
    expect(input("Shell 路径").value).toBe(GIT_BASH);
    expect(input("Shell 启动参数").value).toBe("--login -i");

    click(saveButton());
    await flush();
    await flush();
    expect(mocks.update).toHaveBeenCalledTimes(1);
    expect(mocks.update.mock.calls[0][0].options).toMatchObject({ shell: GIT_BASH, shellArgs: ["--login", "-i"] });
  });

  it("keeps free-text shells custom and drops empty arguments", async () => {
    mountLocalEditor({ shell: "/opt/bin/nu" });
    await flush();
    await flush();
    const picker = mounted!.container.querySelector<HTMLButtonElement>('button[aria-label="选择本机 Shell"]');
    expect(picker?.textContent).toContain("自定义");
    setInputValue(input("Shell 启动参数"), "   ");
    click(saveButton());
    await flush();
    await flush();
    expect(mocks.update).toHaveBeenCalledTimes(1);
    const options = mocks.update.mock.calls[0][0].options as Record<string, unknown>;
    expect(options.shell).toBe("/opt/bin/nu");
    expect(options).not.toHaveProperty("shellArgs");
  });

  it("blocks saving arguments the backend would reject", async () => {
    mountLocalEditor({ shell: "/bin/zsh" });
    await flush();
    setInputValue(input("Shell 启动参数"), Array.from({ length: 33 }, () => "-x").join(" "));
    await flush();
    expect(mounted!.container.querySelector('[role="alert"]')?.textContent).toContain("32");
    click(saveButton());
    await flush();
    expect(mocks.update).not.toHaveBeenCalled();
  });

  it("leaves the free-text fields when the host cannot list its shells", async () => {
    mocks.profiles.mockRejectedValue(new Error("unsupported"));
    mountLocalEditor({});
    await flush();
    await flush();
    expect(mounted!.container.querySelector('button[aria-label="选择本机 Shell"]')).toBeNull();
    expect(input("Shell 路径").value).toBe("");
  });
});
