/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import {
  bytesToBase64,
  clipboardHasPlainText,
  describeImageUploadFailure,
  imageFilesFromDataTransfer,
  markdownImageLink,
} from "../../features/terminal/imagePaste";
import { normalizePublicBaseURL } from "../../features/settings/FilesCard";
import { ImageUploadError } from "../../ipc/webFiles";
import { mockInvoke } from "../../demo/mock";

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

describe("imageFilesFromDataTransfer", () => {
  it("只提取 image/* 文件, 跳过文本项与其他文件", () => {
    const dt = dataTransferWith([
      { kind: "string", type: "text/plain", file: null },
      { kind: "file", type: "image/png", file: png("a.png") },
      { kind: "file", type: "text/plain", file: new File(["x"], "notes.txt", { type: "text/plain" }) },
      { kind: "file", type: "image/webp", file: new File(["w"], "b.webp", { type: "image/webp" }) },
    ]);
    const files = imageFilesFromDataTransfer(dt);
    expect(files.map((f) => f.name)).toEqual(["a.png", "b.webp"]);
  });

  it("空数据与非图片载荷返回空数组, items 缺失时回退到 files", () => {
    expect(imageFilesFromDataTransfer(null)).toEqual([]);
    expect(imageFilesFromDataTransfer(dataTransferWith([]))).toEqual([]);
    const viaFiles = {
      items: [],
      files: [png("c.png")],
      getData: () => "",
    } as unknown as DataTransfer;
    expect(imageFilesFromDataTransfer(viaFiles).map((f) => f.name)).toEqual(["c.png"]);
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

describe("normalizePublicBaseURL", () => {
  it("接受合法地址并去掉尾斜杠", () => {
    expect(normalizePublicBaseURL("")).toEqual({ ok: true, value: "" });
    expect(normalizePublicBaseURL("   ")).toEqual({ ok: true, value: "" });
    expect(normalizePublicBaseURL("https://example.com")).toEqual({
      ok: true,
      value: "https://example.com",
    });
    expect(normalizePublicBaseURL("https://example.com/nexterm/")).toEqual({
      ok: true,
      value: "https://example.com/nexterm",
    });
    expect(normalizePublicBaseURL("http://example.com/")).toEqual({
      ok: true,
      value: "http://example.com",
    });
  });

  it("拒绝与后端 ParsePublicBaseURL 相同的非法输入", () => {
    for (const raw of [
      "ftp://example.com",
      "example.com",
      "https://",
      "https://user:pass@example.com",
      "https://example.com/?q=1",
      "https://example.com/?",
      "https://example.com/#frag",
    ]) {
      const result = normalizePublicBaseURL(raw);
      expect(result.ok, raw).toBe(false);
    }
  });
});

describe("demo mock 文件命令", () => {
  it("files_settings_get/set 回显并持久化演示值", async () => {
    const initial = (await mockInvoke("files_settings_get")) as { publicBaseURL: string };
    expect(initial.publicBaseURL).toBe("");

    const saved = (await mockInvoke("files_settings_set", {
      publicBaseURL: "https://example.com/nexterm/",
    })) as { publicBaseURL: string };
    expect(saved.publicBaseURL).toBe("https://example.com/nexterm");

    const reloaded = (await mockInvoke("files_settings_get")) as { publicBaseURL: string };
    expect(reloaded.publicBaseURL).toBe("https://example.com/nexterm");

    const cleared = (await mockInvoke("files_settings_set", { publicBaseURL: "" })) as {
      publicBaseURL: string;
    };
    expect(cleared.publicBaseURL).toBe("");
  });

  it("files_settings_set 拒绝非法 URL", async () => {
    await expect(mockInvoke("files_settings_set", { publicBaseURL: "ftp://example.com" })).rejects.toMatchObject({
      code: "bad_param",
    });
    await expect(
      mockInvoke("files_settings_set", { publicBaseURL: "https://example.com/?q=1" }),
    ).rejects.toMatchObject({ code: "bad_param" });
  });

  it("files_save_image 回显路径并估算字节数", async () => {
    const saved = (await mockInvoke("files_save_image", {
      path: "/tmp/a.png",
      contentBase64: "aGk=",
    })) as { path: string; bytes: number };
    expect(saved.path).toBe("/tmp/a.png");
    expect(saved.bytes).toBe(2);

    await expect(mockInvoke("files_save_image", { path: "", contentBase64: "aGk=" })).rejects.toMatchObject({
      code: "bad_param",
    });
  });
});
