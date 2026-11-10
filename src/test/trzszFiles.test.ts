/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
// The ESM build, as the app bundle loads it: the CommonJS entry Node picks for
// "trzsz" sees require("fs") and switches trzsz.js to its Node mode.
import { TrzszFilter } from "trzsz/lib/trzsz.mjs";
import {
  installTrzszPickers,
  markTrzszTransferAborted,
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
    pathExists: vi.fn(async () => false),
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
    const d = deps({
      pathExists: vi.fn(async (path: string) => path === "/home/me/Downloads/a.txt"),
    });
    const win: Win = {};
    installTrzszPickers(win, d);
    const root = await saveRoot(win);
    const writer = await (await root.getFileHandle("a.txt", { create: true })).createWritable();
    await writer.write(new TextEncoder().encode("hi"));
    await writer.close();
    // The content is sent once, under the first free name.
    expect(d.writeFile).toHaveBeenCalledTimes(1);
    expect(vi.mocked(d.writeFile).mock.calls[0]).toEqual(["/home/me/Downloads/a.txt.0", btoa("hi")]);
    expect(d.notify).toHaveBeenCalledWith("info", expect.stringContaining("a.txt.0"));
  });

  it("stops looking for a free name after a bounded number of tries", async () => {
    const d = deps({ pathExists: vi.fn(async () => true) });
    const win: Win = {};
    installTrzszPickers(win, d);
    const root = await saveRoot(win);
    const writer = await (await root.getFileHandle("a.txt")).createWritable();
    await writer.write(new Uint8Array(1));
    await writer.close();
    expect(d.writeFile).not.toHaveBeenCalled();
    expect(vi.mocked(d.pathExists).mock.calls.length).toBeLessThanOrEqual(101);
    expect(d.notify).toHaveBeenCalledWith("error", expect.any(String));
  });

  it("saves nothing when a file went over the size limit before close", async () => {
    const d = deps({ desktop: false });
    const win: Win = {};
    installTrzszPickers(win, d);
    const root = await saveRoot(win);
    const writer = await (await root.getFileHandle("big.bin")).createWritable();
    await expect(writer.write(new Uint8Array(TRZSZ_BUFFERED_SAVE_MAX_BYTES + 1))).rejects.toThrow();
    // trzsz.js cleans up by closing the writer.
    await writer.close();
    expect(d.saveBlob).not.toHaveBeenCalled();
    expect(d.writeFile).not.toHaveBeenCalled();
  });

  it("does not save a file that was still being received when the transfer was cancelled", async () => {
    const d = deps();
    const win: Win = {};
    installTrzszPickers(win, d);
    const root = await saveRoot(win);
    const done = await (await root.getFileHandle("done.txt")).createWritable();
    await done.write(new TextEncoder().encode("ok"));
    await done.close();
    const partial = await (await root.getFileHandle("part.txt")).createWritable();
    await partial.write(new TextEncoder().encode("half"));
    markTrzszTransferAborted();
    await partial.close();
    expect(d.writeFile).toHaveBeenCalledTimes(1);
    expect(vi.mocked(d.writeFile).mock.calls[0][0]).toBe("/home/me/Downloads/done.txt");
    expect(d.notify).toHaveBeenCalledWith("info", expect.stringContaining("part.txt"));
  });

  it("refuses a second pending confirmation beyond one waiting", async () => {
    let answer!: (ok: boolean) => void;
    const ask = vi.fn(() => new Promise<boolean>((resolve) => (answer = resolve)));
    const d = deps({ ask });
    const win: Win = {};
    installTrzszPickers(win, d);
    const first = win.showDirectoryPicker!({ id: "trzsz_download" });
    const second = win.showDirectoryPicker!({ id: "trzsz_download" });
    const third = win.showDirectoryPicker!({ id: "trzsz_download" });
    await expect(third).rejects.toMatchObject({ name: "AbortError" });
    expect(ask).toHaveBeenCalledTimes(1);
    answer(false);
    await expect(first).rejects.toMatchObject({ name: "AbortError" });
    await vi.waitFor(() => expect(ask).toHaveBeenCalledTimes(2));
    answer(false);
    await expect(second).rejects.toMatchObject({ name: "AbortError" });
    expect(d.declined).toBeUndefined();
  });

  it("reports a declined request", async () => {
    const declined = vi.fn();
    const d = deps({ ask: vi.fn(async () => false), declined });
    const win: Win = {};
    installTrzszPickers(win, d);
    await expect(win.showDirectoryPicker!({ id: "trzsz_download" })).rejects.toBeTruthy();
    expect(declined).toHaveBeenCalledTimes(1);
  });

  it("rejects Windows device names only on Windows", () => {
    for (const name of ["con", "NUL.txt", "com1", "LPT9.log", "aux.tar.gz"]) {
      expect(safeEntryName(name, true)).toBe(false);
      expect(safeEntryName(name, false)).toBe(true);
    }
    expect(safeEntryName("console.txt", true)).toBe(true);
    expect(safeEntryName("com10", true)).toBe(true);
  });

  it("aborts a download past the file, depth or total size caps with a visible message", async () => {
    const d = deps({ limits: { files: 2, depth: 2, totalBytes: 10 } });
    const win: Win = {};
    installTrzszPickers(win, d);
    const root = (await saveRoot(win)) as unknown as {
      getFileHandle: (n: string) => Promise<unknown>;
      getDirectoryHandle: (n: string) => Promise<typeof root>;
    };
    await root.getFileHandle("1");
    await root.getFileHandle("2");
    await expect(root.getFileHandle("3")).rejects.toThrow();
    expect(d.notify).toHaveBeenCalledWith("error", expect.any(String));

    const d2 = deps({ limits: { depth: 2 } });
    const win2: Win = {};
    uninstallTrzszPickers();
    installTrzszPickers(win2, d2);
    const r2 = (await saveRoot(win2)) as unknown as { getDirectoryHandle: (n: string) => Promise<typeof r2> };
    const level1 = await r2.getDirectoryHandle("a");
    const level2 = await level1.getDirectoryHandle("b");
    await expect(level2.getDirectoryHandle("c")).rejects.toThrow();
    expect(d2.notify).toHaveBeenCalledWith("error", expect.any(String));

    const d3 = deps({ limits: { totalBytes: 10 } });
    const win3: Win = {};
    uninstallTrzszPickers();
    installTrzszPickers(win3, d3);
    const r3 = await saveRoot(win3);
    const w = await (await r3.getFileHandle("x")).createWritable();
    await w.write(new Uint8Array(6));
    await expect(w.write(new Uint8Array(6))).rejects.toThrow();
    await w.close();
    expect(d3.writeFile).not.toHaveBeenCalled();
    expect(d3.notify).toHaveBeenCalledWith("error", expect.any(String));
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
