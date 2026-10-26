/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import {
  bytesToBase64,
  clipboardHasPlainText,
  markdownFileLink,
  pasteFileExtension,
  MAX_PASTE_FILE_BYTES,
  pasteUploadFiles,
  sanitizePasteFileName,
  splitOversizedFiles,
  uniquePasteFileNames,
} from "../../features/terminal/imagePaste";

interface StubItem {
  kind: string;
  type: string;
  file: File | null;
}

function dataTransferWith(items: StubItem[], text = ""): DataTransfer {
  return {
    items: items.map((item) => ({
      kind: item.kind,
      type: item.type,
      getAsFile: () => item.file,
    })),
    files: items.filter((item) => item.kind === "file" && item.file).map((item) => item.file as File),
    getData: (type: string) => (type === "text/plain" ? text : ""),
  } as unknown as DataTransfer;
}

function png(name: string): File {
  return new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });
}

describe("pasteUploadFiles", () => {
  const txt = () => new File(["x"], "notes.txt", { type: "text/plain" });

  it("returns every File object, images and other files alike, in order", () => {
    const dt = dataTransferWith([
      { kind: "file", type: "image/png", file: png("a.png") },
      { kind: "file", type: "text/plain", file: txt() },
    ]);
    expect(pasteUploadFiles(dt).map((f) => f.name)).toEqual(["a.png", "notes.txt"]);
  });

  it("returns nothing for plain text and empty or null payloads", () => {
    expect(pasteUploadFiles(dataTransferWith([{ kind: "string", type: "text/plain", file: null }], "echo hi"))).toEqual([]);
    expect(pasteUploadFiles(null)).toEqual([]);
    expect(pasteUploadFiles(dataTransferWith([]))).toEqual([]);
    expect(pasteUploadFiles(dataTransferWith([{ kind: "file", type: "image/png", file: null }]))).toEqual([]);
  });

  it("prefers files over text when a non-image File is present", () => {
    const dt = dataTransferWith([{ kind: "file", type: "application/pdf", file: new File(["p"], "d.pdf") }], "/home/me/d.pdf");
    expect(pasteUploadFiles(dt).map((f) => f.name)).toEqual(["d.pdf"]);
    const mixed = dataTransferWith(
      [
        { kind: "file", type: "image/png", file: png("icon.png") },
        { kind: "file", type: "text/plain", file: txt() },
      ],
      "notes.txt",
    );
    expect(pasteUploadFiles(mixed).map((f) => f.name)).toEqual(["icon.png", "notes.txt"]);
  });

  it("keeps the text paste when the only files are images next to plain text (spreadsheet or web copy)", () => {
    const dt = dataTransferWith([{ kind: "file", type: "image/png", file: png("a.png") }], "cell value");
    expect(pasteUploadFiles(dt)).toEqual([]);
  });

  it("falls back to files when items is missing", () => {
    const dt = { items: [], files: [txt()], getData: () => "" } as unknown as DataTransfer;
    expect(pasteUploadFiles(dt).map((f) => f.name)).toEqual(["notes.txt"]);
  });
});

describe("splitOversizedFiles", () => {
  it("keeps files at the limit and skips larger ones", () => {
    const at = new File([new Uint8Array(0)], "at.bin");
    Object.defineProperty(at, "size", { value: MAX_PASTE_FILE_BYTES });
    const over = new File([new Uint8Array(0)], "over.bin");
    Object.defineProperty(over, "size", { value: MAX_PASTE_FILE_BYTES + 1 });
    const { accepted, skipped } = splitOversizedFiles([at, over]);
    expect(accepted.map((f) => f.name)).toEqual(["at.bin"]);
    expect(skipped.map((f) => f.name)).toEqual(["over.bin"]);
  });
});

describe("remote file names", () => {
  it("removes separators, control characters and leading dots", () => {
    expect(sanitizePasteFileName("../../etc/passwd")).toBe("_.._etc_passwd");
    expect(sanitizePasteFileName(".bashrc")).toBe("bashrc");
    expect(sanitizePasteFileName("a\u0000b\nc.txt")).toBe("a_b_c.txt");
    expect(sanitizePasteFileName("   ")).toBe("file");
    expect(sanitizePasteFileName("")).toBe("file");
  });

  it("limits length while keeping the extension", () => {
    const name = sanitizePasteFileName(`${"a".repeat(300)}.tar`);
    expect(name.length).toBeLessThanOrEqual(100);
    expect(name.endsWith(".tar")).toBe(true);
  });

  it("makes duplicate names unique within one paste", () => {
    expect(uniquePasteFileNames(["a.txt", "a.txt", "A.txt", "b"])).toEqual(["a.txt", "a-2.txt", "A-3.txt", "b"]);
  });
});

describe("clipboardHasPlainText", () => {
  it("detects plain text in mixed clipboard data and lets the terminal handle the paste", () => {
    expect(clipboardHasPlainText(dataTransferWith([], "echo hi"))).toBe(true);
    expect(clipboardHasPlainText(dataTransferWith([], ""))).toBe(false);
    expect(clipboardHasPlainText(null)).toBe(false);
  });
});

describe("markdownFileLink", () => {
  it("creates a regular Markdown link, not an image embed, and escapes the text", () => {
    expect(markdownFileLink("shot.png", "/tmp/nexterm-paste-1.png")).toBe(
      "[shot.png](/tmp/nexterm-paste-1.png)",
    );
    expect(markdownFileLink("a[b]c.png", "/tmp/a.png")).toBe("[a\\[b\\]c.png](/tmp/a.png)");
    expect(markdownFileLink("", "/tmp/a.png")).toBe("[file](/tmp/a.png)");
  });

  it("wraps targets containing spaces or parentheses in angle brackets", () => {
    expect(markdownFileLink("a.png", "/tmp/my images/a.png")).toBe("[a.png](</tmp/my images/a.png>)");
    expect(markdownFileLink("a.png", "/tmp/a(1).png")).toBe("[a.png](</tmp/a(1).png>)");
  });
});

describe("pasteFileExtension", () => {
  it("prefers the filename extension and lowercases it", () => {
    expect(pasteFileExtension(png("shot.PNG"))).toBe("png");
    expect(pasteFileExtension(new File(["w"], "b.webp", { type: "image/webp" }))).toBe("webp");
  });

  it("falls back to the MIME subtype and then png when the filename has no extension", () => {
    expect(pasteFileExtension(new File(["x"], "pasted-image", { type: "image/gif" }))).toBe("gif");
    expect(pasteFileExtension(new File(["x"], "pasted-image", { type: "image/svg+xml" }))).toBe("png");
    expect(pasteFileExtension(new File(["x"], "pasted-image"))).toBe("png");
  });
});

describe("bytesToBase64", () => {
  it("round-trips small and chunked data", () => {
    const small = new Uint8Array([1, 2, 3, 250]);
    expect(bytesToBase64(small)).toBe(btoa(String.fromCharCode(...small)));

    const large = new Uint8Array(0x8000 + 17);
    for (let i = 0; i < large.length; i++) large[i] = i % 251;
    const decoded = atob(bytesToBase64(large));
    expect(decoded.length).toBe(large.length);
    for (let i = 0; i < large.length; i++) expect(decoded.charCodeAt(i)).toBe(large[i]);
  });
});
