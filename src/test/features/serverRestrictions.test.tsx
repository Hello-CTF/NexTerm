/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, type MountedView } from "./reactTestUtils";
import type { Asset } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({
  groupList: vi.fn(),
  listCredentials: vi.fn(),
  vaultStatus: vi.fn(),
}));
vi.mock("../../ipc/webFiles", () => ({
  browserFilesAvailable: () => true,
  pickBrowserFile: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
  assetApi: { groupList: mocks.groupList, create: vi.fn(), readKeyFile: vi.fn() },
  vaultApi: { listCredentials: mocks.listCredentials, status: mocks.vaultStatus, setCredential: vi.fn(), unlock: vi.fn() },
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
}));

import { NewCredentialModal } from "../../features/credentials/NewCredentialModal";
import { AssetEditor } from "../../features/explorer/AssetTree";
import { restrictTemplateForServer, type ConnectTemplate } from "../../features/explorer/connectTemplates";
import { useAuth } from "../../features/auth/store";
import { SERVER_RESTRICTION_TEXT } from "../../features/auth/useServerAccountRestrictions";

const ASSET: Asset = {
  id: "a1",
  groupId: null,
  kind: "ssh",
  name: "web-01",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "password",
  keyPath: null,
  credId: null,
  options: { proxyCommand: "nc %h %p", forwardAgent: true, agentSocket: "/run/agent.sock", certPath: "/keys/c.pub" },
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};

function signIn(on: boolean) {
  useAuth.setState({ user: on ? ({ id: "u1", username: "alice" } as never) : null });
}

function editor(initial?: Asset): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(AssetEditor, { kind: "asset", initial, onClose: vi.fn(), onSaved: vi.fn(), presetGroupId: null }),
    ),
  );
}

function openAdvanced(c: HTMLElement) {
  const toggle = [...c.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].find((b) => b.textContent?.includes("高级选项"))!;
  if (toggle.getAttribute("aria-expanded") !== "true") click(toggle);
}

function input(c: HTMLElement, label: string): HTMLInputElement {
  const el = c.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`);
  if (!el) throw new Error(`${label} not found`);
  return el;
}

let mounted: MountedView | undefined;
beforeEach(() => {
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "master", unlocked: true });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  signIn(false);
});

describe("AssetEditor on a multi-user server", () => {
  it("disables the restricted SSH options, keeps old values visible and explains why", async () => {
    signIn(true);
    mounted = editor(ASSET);
    await flush();
    const c = mounted.container;
    expect(input(c, "ProxyCommand").value).toBe("nc %h %p");
    expect(input(c, "ProxyCommand").disabled).toBe(true);
    expect(input(c, "Agent socket").value).toBe("/run/agent.sock");
    expect(input(c, "Agent socket").disabled).toBe(true);
    expect(c.textContent).toContain(SERVER_RESTRICTION_TEXT.proxyCommand);
    expect(c.textContent).toContain(SERVER_RESTRICTION_TEXT.forwardAgent);
  });

  it("lets the user clear an old restricted value", async () => {
    signIn(true);
    mounted = editor(ASSET);
    await flush();
    const note = [...mounted.container.querySelectorAll('[role="note"]')].find((n) =>
      n.textContent?.includes(SERVER_RESTRICTION_TEXT.proxyCommand),
    );
    click(note!.querySelector("button")!);
    await flush();
    expect(input(mounted.container, "ProxyCommand").value).toBe("");
  });

  it("disables ssh-agent forwarding only until it is switched off", async () => {
    signIn(true);
    mounted = editor({ ...ASSET, options: {} });
    await flush();
    openAdvanced(mounted.container);
    await flush();
    expect(input(mounted.container, "ssh-agent 转发").disabled).toBe(true);
  });

  it("leaves everything editable without a signed-in account", async () => {
    signIn(false);
    mounted = editor({ ...ASSET, options: { proxyCommand: "nc %h %p" } });
    await flush();
    openAdvanced(mounted.container);
    await flush();
    expect(input(mounted.container, "ProxyCommand").disabled).toBe(false);
    expect(mounted.container.textContent).not.toContain("多用户服务端不支持");
  });
});

describe("NewCredentialModal on a multi-user server", () => {
  async function openKeyForm(): Promise<HTMLElement> {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(QueryClientProvider, { client }, createElement(NewCredentialModal, { onClose: vi.fn(), onSaved: vi.fn() })),
    );
    const btn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === "私钥");
    click(btn!);
    await flush();
    return mounted.container;
  }
  const refButton = (c: HTMLElement) =>
    [...c.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("引用"))!;

  it("disables referencing a server file", async () => {
    signIn(true);
    const c = await openKeyForm();
    expect(refButton(c).disabled).toBe(true);
    expect(c.textContent).toContain(SERVER_RESTRICTION_TEXT.fileCredential);
  });

  it("keeps file references available otherwise", async () => {
    signIn(false);
    const c = await openKeyForm();
    expect(refButton(c).disabled).toBe(false);
  });
});

describe("restrictTemplateForServer", () => {
  const base: ConnectTemplate = {
    id: "t", name: "t", kind: "ssh", port: 22, username: "", authKind: "agent", keyOrigin: "ref", keyPath: "/k",
    vaultCredId: "", jumpAssetId: "", proxyCommand: "nc", forwardAgent: true, agentSocket: "/s", certPath: "/c",
    startupCommand: "", encoding: "utf-8", envText: "",
  };
  it("drops every field the server rejects", () => {
    const { template, dropped } = restrictTemplateForServer(base);
    expect(template).toMatchObject({
      authKind: "password", keyOrigin: "vault", keyPath: "", proxyCommand: "", forwardAgent: false, agentSocket: "", certPath: "",
    });
    expect(dropped.length).toBeGreaterThan(0);
  });
  it("moves a local template to ssh and leaves a clean one alone", () => {
    expect(restrictTemplateForServer({ ...base, kind: "local" }).template.kind).toBe("ssh");
    const clean = { ...base, authKind: "password", keyOrigin: "vault" as const, keyPath: "", proxyCommand: "", forwardAgent: false, agentSocket: "", certPath: "" };
    expect(restrictTemplateForServer(clean)).toEqual({ template: clean, dropped: [] });
  });
});
