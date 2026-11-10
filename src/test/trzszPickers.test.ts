/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TrzszFileDeps } from "../features/terminal/trzsz/trzszFiles";

const mocks = vi.hoisted(() => ({
  install: vi.fn(),
  listFolder: vi.fn(),
  readRange: vi.fn(),
  write: vi.fn(),
  ask: vi.fn(),
  pushToast: vi.fn(),
  declined: vi.fn(),
  context: vi.fn(),
  openFiles: vi.fn(),
  openFolder: vi.fn(),
  saveBlob: vi.fn(),
}));

vi.mock("../features/terminal/trzsz/trzszFiles", () => ({ installTrzszPickers: mocks.install }));
vi.mock("../features/terminal/trzsz/trzszSession", () => ({
  currentTrzszRequestContext: mocks.context,
  reportTrzszDeclined: mocks.declined,
}));
vi.mock("../ipc/env", () => ({ DESKTOP: true }));
vi.mock("../app/platform", () => ({ isWindows: () => true }));
vi.mock("../ipc/commands", () => ({
  localFileApi: { listFolder: mocks.listFolder, readRange: mocks.readRange, write: mocks.write },
}));
vi.mock("../ipc/wails", () => ({ openWailsFiles: mocks.openFiles, openWailsFolder: mocks.openFolder }));
vi.mock("../ipc/webFiles", () => ({ saveBrowserBlob: mocks.saveBlob }));
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../app/store", () => ({ useUi: { getState: () => ({ pushToast: mocks.pushToast }) } }));

import { installDefaultTrzszPickers } from "../features/terminal/trzsz/trzszPickers";

function installed(): TrzszFileDeps {
  installDefaultTrzszPickers();
  expect(mocks.install).toHaveBeenCalledTimes(1);
  const [win, deps] = mocks.install.mock.calls[0] as [unknown, TrzszFileDeps];
  expect(win).toBe(window);
  return deps;
}

beforeEach(() => vi.clearAllMocks());
afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = "";
});

describe("installDefaultTrzszPickers", () => {
  it("passes the platform flags and forwards the dialog, toast and folder calls", async () => {
    const deps = installed();
    expect(deps.desktop).toBe(true);
    expect(deps.windows).toBe(true);
    expect(deps.context).toBe(mocks.context);
    expect(deps.declined).toBe(mocks.declined);
    expect(deps.openFiles).toBe(mocks.openFiles);
    expect(deps.openFolder).toBe(mocks.openFolder);
    expect(deps.saveBlob).toBe(mocks.saveBlob);

    mocks.ask.mockResolvedValue(true);
    await expect(deps.ask("go?", { title: "t", kind: "warning" })).resolves.toBe(true);
    expect(mocks.ask).toHaveBeenCalledWith("go?", { title: "t", kind: "warning" });

    deps.notify("error", "boom");
    expect(mocks.pushToast).toHaveBeenCalledWith("error", "boom");

    const listing = { name: "d", files: [], emptyDirs: [] };
    mocks.listFolder.mockResolvedValue(listing);
    await expect(deps.listFolder("/d")).resolves.toBe(listing);
    expect(mocks.listFolder).toHaveBeenCalledWith("/d");

    mocks.readRange.mockResolvedValue({ size: 3, contentBase64: "YWJj" });
    await expect(deps.readRange("/f", 1, 2)).resolves.toEqual({ size: 3, contentBase64: "YWJj" });
    expect(mocks.readRange).toHaveBeenCalledWith("/f", 1, 2);

    mocks.write.mockResolvedValue({ exists: true });
    await expect(deps.writeFile("/f", "YQ==")).resolves.toEqual({ exists: true });
    expect(mocks.write).toHaveBeenCalledWith("/f", "YQ==");
  });

  it("reports pathExists from whether a one-byte read succeeds", async () => {
    const deps = installed();
    mocks.readRange.mockResolvedValueOnce({ size: 1, contentBase64: "YQ==" });
    await expect(deps.pathExists("/there")).resolves.toBe(true);
    expect(mocks.readRange).toHaveBeenCalledWith("/there", 0, 1);
    mocks.readRange.mockRejectedValueOnce(new Error("missing"));
    await expect(deps.pathExists("/gone")).resolves.toBe(false);
  });

  describe("pickBrowserFiles", () => {
    function pending(directory: boolean) {
      const promise = installed().pickBrowserFiles(directory);
      const input = document.querySelector<HTMLInputElement>('input[type="file"]')!;
      expect(input).not.toBeNull();
      return { promise, input };
    }
    function setFiles(input: HTMLInputElement, files: File[]) {
      Object.defineProperty(input, "files", { configurable: true, value: files });
    }

    it("resolves with the chosen files and removes the input", async () => {
      const { promise, input } = pending(false);
      expect(input.multiple).toBe(true);
      expect(input.hasAttribute("webkitdirectory")).toBe(false);
      const file = new File(["a"], "a.txt");
      setFiles(input, [file]);
      input.dispatchEvent(new Event("change"));
      await expect(promise).resolves.toEqual([file]);
      expect(document.querySelector('input[type="file"]')).toBeNull();
    });

    it("marks the input as a directory picker when asked", async () => {
      const { promise, input } = pending(true);
      expect(input.hasAttribute("webkitdirectory")).toBe(true);
      input.dispatchEvent(new Event("cancel"));
      await expect(promise).resolves.toBeNull();
    });

    it("resolves null when change fires with no files", async () => {
      const { promise, input } = pending(false);
      setFiles(input, []);
      input.dispatchEvent(new Event("change"));
      await expect(promise).resolves.toBeNull();
    });

    it("resolves null when the window regains focus without a selection", async () => {
      vi.useFakeTimers();
      const { promise, input } = pending(false);
      setFiles(input, []);
      window.dispatchEvent(new Event("focus"));
      await vi.advanceTimersByTimeAsync(800);
      await expect(promise).resolves.toBeNull();
      expect(document.querySelector('input[type="file"]')).toBeNull();
    });

    it("keeps waiting after focus when files were chosen and settles only once", async () => {
      vi.useFakeTimers();
      const { promise, input } = pending(false);
      const file = new File(["b"], "b.txt");
      setFiles(input, [file]);
      window.dispatchEvent(new Event("focus"));
      await vi.advanceTimersByTimeAsync(800);
      input.dispatchEvent(new Event("change"));
      await expect(promise).resolves.toEqual([file]);
      input.dispatchEvent(new Event("cancel"));
    });
  });
});
