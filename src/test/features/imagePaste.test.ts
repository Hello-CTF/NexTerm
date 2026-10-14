/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import {
  bytesToBase64,
  clipboardHasPlainText,
  markdownFileLink,
  pasteFileExtension,
  pureImageFiles,
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

describe("pureImageFiles", () => {
  it("returns files only when every file item is an image", () => {
    const dt = dataTransferWith([
      { kind: "file", type: "image/png", file: png("a.png") },
      { kind: "file", type: "image/webp", file: new File(["w"], "b.webp", { type: "image/webp" }) },
    ]);
    expect(pureImageFiles(dt).map((f) => f.name)).toEqual(["a.png", "b.webp"]);
  });

  it("returns an empty list for mixed image and non-image payloads instead of extracting an image subset", () => {
    const dt = dataTransferWith([
      { kind: "file", type: "image/png", file: png("a.png") },
      { kind: "file", type: "text/plain", file: new File(["x"], "notes.txt", { type: "text/plain" }) },
    ]);
    expect(pureImageFiles(dt)).toEqual([]);
  });

  it("ignores text items and returns empty arrays for empty and non-image payloads", () => {
    const dt = dataTransferWith([
      { kind: "string", type: "text/plain", file: null },
      { kind: "file", type: "image/png", file: png("a.png") },
    ]);
    expect(pureImageFiles(dt).map((f) => f.name)).toEqual(["a.png"]);
    expect(pureImageFiles(null)).toEqual([]);
    expect(pureImageFiles(dataTransferWith([]))).toEqual([]);
    expect(
      pureImageFiles(
        dataTransferWith([{ kind: "file", type: "application/pdf", file: new File(["p"], "d.pdf") }]),
      ),
    ).toEqual([]);
  });

  it("falls back to files when items is missing, preserving all-or-nothing behavior", () => {
    const allImages = {
      items: [],
      files: [png("c.png")],
      getData: () => "",
    } as unknown as DataTransfer;
    expect(pureImageFiles(allImages).map((f) => f.name)).toEqual(["c.png"]);
    const mixed = {
      items: [],
      files: [png("c.png"), new File(["x"], "notes.txt", { type: "text/plain" })],
      getData: () => "",
    } as unknown as DataTransfer;
    expect(pureImageFiles(mixed)).toEqual([]);
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
