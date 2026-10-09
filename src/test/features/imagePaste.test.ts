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
  it("全部文件项都是图片时才返回文件列表", () => {
    const dt = dataTransferWith([
      { kind: "file", type: "image/png", file: png("a.png") },
      { kind: "file", type: "image/webp", file: new File(["w"], "b.webp", { type: "image/webp" }) },
    ]);
    expect(pureImageFiles(dt).map((f) => f.name)).toEqual(["a.png", "b.webp"]);
  });

  it("混合载荷(图片+非图片)整体返回空, 不提取图片子集", () => {
    const dt = dataTransferWith([
      { kind: "file", type: "image/png", file: png("a.png") },
      { kind: "file", type: "text/plain", file: new File(["x"], "notes.txt", { type: "text/plain" }) },
    ]);
    expect(pureImageFiles(dt)).toEqual([]);
  });

  it("文本项不计入文件判定, 空数据与非图片载荷返回空数组", () => {
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

  it("items 缺失时回退到 files, 同样 all-or-nothing", () => {
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
  it("混合剪贴板里有纯文本时判定为真, 交还终端默认粘贴", () => {
    expect(clipboardHasPlainText(dataTransferWith([], "echo hi"))).toBe(true);
    expect(clipboardHasPlainText(dataTransferWith([], ""))).toBe(false);
    expect(clipboardHasPlainText(null)).toBe(false);
  });
});

describe("markdownFileLink", () => {
  it("生成普通 Markdown 链接(非图片嵌入)并转义文本", () => {
    expect(markdownFileLink("shot.png", "/tmp/nexterm-paste-1.png")).toBe(
      "[shot.png](/tmp/nexterm-paste-1.png)",
    );
    expect(markdownFileLink("a[b]c.png", "/tmp/a.png")).toBe("[a\\[b\\]c.png](/tmp/a.png)");
    expect(markdownFileLink("", "/tmp/a.png")).toBe("[file](/tmp/a.png)");
  });

  it("目标含空白或圆括号时用尖括号包裹", () => {
    expect(markdownFileLink("a.png", "/tmp/my images/a.png")).toBe("[a.png](</tmp/my images/a.png>)");
    expect(markdownFileLink("a.png", "/tmp/a(1).png")).toBe("[a.png](</tmp/a(1).png>)");
  });
});

describe("pasteFileExtension", () => {
  it("优先取文件名扩展名并小写化", () => {
    expect(pasteFileExtension(png("shot.PNG"))).toBe("png");
    expect(pasteFileExtension(new File(["w"], "b.webp", { type: "image/webp" }))).toBe("webp");
  });

  it("文件名没有扩展名时回退 MIME 子类型, 再缺省 png", () => {
    expect(pasteFileExtension(new File(["x"], "pasted-image", { type: "image/gif" }))).toBe("gif");
    expect(pasteFileExtension(new File(["x"], "pasted-image", { type: "image/svg+xml" }))).toBe("png");
    expect(pasteFileExtension(new File(["x"], "pasted-image"))).toBe("png");
  });
});

describe("bytesToBase64", () => {
  it("小数据与跨chunk数据都能往返", () => {
    const small = new Uint8Array([1, 2, 3, 250]);
    expect(bytesToBase64(small)).toBe(btoa(String.fromCharCode(...small)));

    const large = new Uint8Array(0x8000 + 17);
    for (let i = 0; i < large.length; i++) large[i] = i % 251;
    const decoded = atob(bytesToBase64(large));
    expect(decoded.length).toBe(large.length);
    for (let i = 0; i < large.length; i++) expect(decoded.charCodeAt(i)).toBe(large[i]);
  });
});
