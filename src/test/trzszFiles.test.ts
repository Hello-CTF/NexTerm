/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
// The ESM build, as the app bundle loads it: the CommonJS entry Node picks for
// "trzsz" sees require("fs") and switches trzsz.js to its Node mode.
import { TrzszFilter } from "trzsz/lib/trzsz.mjs";
import {
  installTrzszPickers,
  safeEntryName,
  TRZSZ_BUFFERED_SAVE_MAX_BYTES,
  uninstallTrzszPickers,
  type TrzszFileDeps,
} from "../features/terminal/trzsz/trzszFiles";

type Win = { showOpenFilePicker?: (o?: unknown) => Promise<unknown>; showDirectoryPicker?: (o?: unknown) => Promise<unknown> };

function deps(overrides: Partial<TrzszFileDeps> = {}): TrzszFileDeps {
  return {
    desktop: true,
    windows: false,
    ask: vi.fn(async () => true),
    notify: vi.fn(),
    context: () => ({ host: "db-1" }),
    openFiles: vi.fn(async () => []),
    openFolder: vi.fn(async () => "/home/me/Downloads"),
    listFolder: vi.fn(async () => ({ name: "x", files: [], emptyDirs: [] })),
    readRange: vi.fn(async () => ({ size: 0, contentBase64: "" })),
    writeFile: vi.fn(async () => ({ exists: false })),
    pickBrowserFiles: vi.fn(async () => null),
    saveBlob: vi.fn(async () => true),
    ...overrides,
  };
}

afterEach(() => uninstallTrzszPickers());

async function saveRoot(win: Win) {
  return (await win.showDirectoryPicker!({ id: "trzsz_download", mode: "readwrite" })) as {
    getFileHandle: (name: string, o?: unknown) => Promise<{ createWritable: () => Promise<{ write: (d: Uint8Array) => Promise<void>; close: () => Promise<void> }> }>;
    getDirectoryHandle: (name: string, o?: unknown) => Promise<unknown>;
  };
}

describe("trzsz pickers", () => {
  it("asks before every save and cancels when the user declines", async () => {
    const win: Win = {};
    const d = deps({ ask: vi.fn(async () => false) });
    installTrzszPickers(win, d);
    await expect(win.showDirectoryPicker!({ id: "trzsz_download" })).rejects.toMatchObject({ name: "AbortError" });
    expect(d.ask).toHaveBeenCalledTimes(1);
    expect(vi.mocked(d.ask).mock.calls[0][0]).toContain("db-1");
    expect(d.openFolder).not.toHaveBeenCalled();
  });

  it("leaves other callers with the browser's own picker", async () => {
    const native = vi.fn(async () => "native");
    const win: Win = { showDirectoryPicker: native };
    installTrzszPickers(win, deps());
    await expect(win.showDirectoryPicker!({ id: "other" })).resolves.toBe("native");
    expect(native).toHaveBeenCalledTimes(1);
  });

  it("saves into the chosen desktop folder without replacing an existing file", async () => {
    const writes: string[] = [];
    const d = deps({
      writeFile: vi.fn(async (path: string) => {
        writes.push(path);
        return { exists: writes.length === 1 };
      }),
    });
    const win: Win = {};
    installTrzszPickers(win, d);
    const root = await saveRoot(win);
    const writer = await (await root.getFileHandle("a.txt", { create: true })).createWritable();
    await writer.write(new TextEncoder().encode("hi"));
    await writer.close();
    expect(writes).toEqual(["/home/me/Downloads/a.txt", "/home/me/Downloads/a.txt.0"]);
    expect(vi.mocked(d.writeFile).mock.calls[1][1]).toBe(btoa("hi"));
    expect(d.notify).toHaveBeenCalledWith("info", expect.stringContaining("a.txt.0"));
  });

  it("refuses names from the host that leave the chosen folder", async () => {
    const win: Win = {};
    installTrzszPickers(win, deps());
    const root = await saveRoot(win);
    for (const name of ["..", "../x", "a/b", "a\\b", "", "x\u0000"]) {
      await expect(root.getFileHandle(name, { create: true })).rejects.toThrow();
      await expect(root.getDirectoryHandle(name, { create: true })).rejects.toThrow();
    }
    expect(safeEntryName("report.csv", false)).toBe(true);
    expect(safeEntryName("a:b", true)).toBe(false);
  });

  it("stops a buffered save past the size limit", async () => {
    const d = deps();
    const win: Win = {};
    installTrzszPickers(win, d);
    const root = await saveRoot(win);
    const writer = await (await root.getFileHandle("big.bin")).createWritable();
    await expect(writer.write(new Uint8Array(TRZSZ_BUFFERED_SAVE_MAX_BYTES + 1))).rejects.toThrow();
  });

  it("downloads files in a browser without the File System Access API, but no folders", async () => {
    const d = deps({ desktop: false });
    const win: Win = {};
    installTrzszPickers(win, d);
    const root = await saveRoot(win);
    const writer = await (await root.getFileHandle("a.txt")).createWritable();
    await writer.write(new TextEncoder().encode("hi"));
    await writer.close();
    expect(d.saveBlob).toHaveBeenCalledWith("a.txt", expect.any(Blob));
    await expect(root.getDirectoryHandle("dir", { create: true })).rejects.toThrow();
  });

  it("uploads browser files picked after confirmation", async () => {
    const file = new File(["data"], "a.txt");
    const d = deps({ desktop: false, pickBrowserFiles: vi.fn(async () => [file]) });
    const win: Win = {};
    installTrzszPickers(win, d);
    const handles = (await win.showOpenFilePicker!({ id: "trzsz_upload", multiple: true })) as { getFile: () => Promise<File> }[];
    expect(d.ask).toHaveBeenCalledTimes(1);
    await expect(handles[0].getFile()).resolves.toBe(file);
  });

  it("reads desktop uploads lazily in range chunks", async () => {
    const content = "hello world";
    const d = deps({
      openFiles: vi.fn(async () => ["/tmp/h.txt"]),
      readRange: vi.fn(async (_path: string, offset: number, max: number) => ({
        size: content.length,
        contentBase64: btoa(content.slice(offset, offset + max)),
      })),
    });
    const win: Win = {};
    installTrzszPickers(win, d);
    const [handle] = (await win.showOpenFilePicker!({ id: "trzsz_upload" })) as { getFile: () => Promise<{ name: string; size: number; slice: (a: number, b: number) => Blob }> }[];
    const file = await handle.getFile();
    expect(file.name).toBe("h.txt");
    expect(file.size).toBe(content.length);
    expect(new TextDecoder().decode(await file.slice(6, 11).arrayBuffer())).toBe("world");
  });

  it("answers a real trzsz.js download request through the confirmation", async () => {
    const ask = vi.fn(async () => false);
    installTrzszPickers(window as unknown as Win, deps({ ask }));
    const sent: string[] = [];
    const filter = new TrzszFilter({
      writeToTerminal: () => undefined,
      sendToServer: (input) => sent.push(typeof input === "string" ? input : new TextDecoder().decode(input)),
    });
    // Built in the page's realm: jsdom's TextEncoder returns another realm's Uint8Array.
    filter.processServerOutput(Uint8Array.from(new TextEncoder().encode("::TRZSZ:TRANSFER:S:1.1.6:1234567890123\r\n")));
    await vi.waitFor(() => expect(ask).toHaveBeenCalledTimes(1));
    // Declining tells tsz on the host that the transfer was refused.
    await vi.waitFor(() => expect(sent.join("")).toContain("#ACT:"));
    await vi.waitFor(() => expect(filter.isTransferringFiles()).toBe(false));
  });
});
