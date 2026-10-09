/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import {
  bytesToBase64,
  clipboardHasPlainText,
  describeImageUploadFailure,
  markdownImageLink,
  pureImageFiles,
} from "../../features/terminal/imagePaste";
import { normalizePublicBaseURL } from "../../features/settings/FilesCard";
import { ImageUploadError } from "../../ipc/webFiles";

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

describe("markdownImageLink", () => {
  it("生成 Markdown 图片链接并转义 alt 文本", () => {
    expect(markdownImageLink("shot.png", "/files/image/01J")).toBe("![shot.png](/files/image/01J)");
    expect(markdownImageLink("a[b]c.png", "/files/image/01J")).toBe("![a\\[b\\]c.png](/files/image/01J)");
    expect(markdownImageLink("", "/files/image/01J")).toBe("![image](/files/image/01J)");
  });

  it("目标含空白或圆括号时用尖括号包裹", () => {
    expect(markdownImageLink("a.png", "/tmp/my images/a.png")).toBe(
      "![a.png](</tmp/my images/a.png>)",
    );
    expect(markdownImageLink("a.png", "/tmp/a(1).png")).toBe("![a.png](</tmp/a(1).png>)");
  });
});

describe("describeImageUploadFailure", () => {
  it("按 HTTP 状态映射成可操作提示", () => {
    expect(describeImageUploadFailure(new ImageUploadError(401, '{"error":{"message":"会话无效或缺失"}}')))
      .toContain("需要登录或没有权限");
    expect(describeImageUploadFailure(new ImageUploadError(401, '{"error":{"message":"会话无效或缺失"}}')))
      .toContain("会话无效或缺失");
    expect(describeImageUploadFailure(new ImageUploadError(413, "image exceeds the maximum allowed size")))
      .toContain("大小限制");
    expect(describeImageUploadFailure(new ImageUploadError(415, "unsupported image type")))
      .toContain("png/jpeg/gif/webp");
    expect(describeImageUploadFailure(new ImageUploadError(507, "quota exceeded"))).toContain("配额");
    expect(describeImageUploadFailure(new ImageUploadError(500, "boom"))).toContain("HTTP 500");
  });

  it("网络错误映射为不可达提示", () => {
    expect(describeImageUploadFailure(new Error("fetch failed"))).toContain("网络或服务不可达");
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

// 与 internal/app/production/files_ipc_test.go 的 TestFilesSettingsSetValidationTable 共享同一张边界用例表,
// 保证前端预检与后端 core.ParsePublicBaseURL 判定一致。
const PUBLIC_BASE_URL_TABLE: Array<{ raw: string; ok: boolean; value?: string }> = [
  { raw: "", ok: true, value: "" },
  { raw: "https://example.com", ok: true, value: "https://example.com" },
  { raw: "https://example.com/", ok: true, value: "https://example.com" },
  { raw: "https://example.com//", ok: true, value: "https://example.com" },
  { raw: "https://example.com/nexterm/", ok: true, value: "https://example.com/nexterm" },
  { raw: "https://example.com/nexterm//", ok: true, value: "https://example.com/nexterm" },
  { raw: "http://example.com", ok: true, value: "http://example.com" },
  { raw: "ftp://example.com", ok: false },
  { raw: "example.com", ok: false },
  { raw: "https://", ok: false },
  { raw: "https://user:pass@example.com", ok: false },
  { raw: "https://@example.com", ok: false },
  { raw: "https://example.com/?q=1", ok: false },
  { raw: "https://example.com/?", ok: false },
  { raw: "https://example.com/#frag", ok: false },
  { raw: "https://example.com/%zz", ok: false },
];

describe("normalizePublicBaseURL 与后端共享边界表", () => {
  it("合法地址按规范化接受", () => {
    for (const tc of PUBLIC_BASE_URL_TABLE.filter((entry) => entry.ok)) {
      expect(normalizePublicBaseURL(tc.raw), tc.raw).toEqual({ ok: true, value: tc.value });
    }
  });

  it("非法地址一律拒绝", () => {
    for (const tc of PUBLIC_BASE_URL_TABLE.filter((entry) => !entry.ok)) {
      const result = normalizePublicBaseURL(tc.raw);
      expect(result.ok, tc.raw).toBe(false);
    }
  });
});
