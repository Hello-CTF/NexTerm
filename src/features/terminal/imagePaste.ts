import { ImageUploadError } from "../../ipc/webFiles";

export { bytesToBase64 } from "../../ui/base64";

// pureImageFiles 仅在全部文件项都是图片且至少一张时返回文件列表(all-or-nothing)。
// 混合载荷(图片+非图片文件)返回空数组, 整个事件交还默认处理, 不提取图片子集、不丢弃任何内容。
export function pureImageFiles(data: DataTransfer | null): File[] {
  if (!data) return [];
  const entries: { type: string; file: File | null }[] = [];
  if (data.items && data.items.length > 0) {
    for (const item of data.items) {
      if (item.kind !== "file") continue;
      entries.push({ type: item.type, file: item.getAsFile() });
    }
  } else {
    for (const file of data.files ?? []) {
      entries.push({ type: file.type, file });
    }
  }
  if (entries.length === 0) return [];
  if (entries.some((entry) => !entry.type.startsWith("image/") || entry.file === null)) return [];
  return entries.map((entry) => entry.file as File);
}

// clipboardHasPlainText 判断混合剪贴板里是否有可用纯文本:
// 有文本时把粘贴交还给终端默认处理, 保证普通文本与 bracketed paste 永不丢失。
export function clipboardHasPlainText(data: DataTransfer | null): boolean {
  if (!data) return false;
  try {
    return data.getData("text/plain").length > 0;
  } catch {
    return false;
  }
}

// markdownImageLink 生成插入终端光标的 Markdown 图片链接。
// alt 文本转义反斜杠与方括号; 目标含空白或圆括号时用尖括号包裹(CommonMark 合法目标)。
export function markdownImageLink(name: string, url: string): string {
  const alt = (name || "image").replace(/([\\\[\]])/g, "\\$1");
  const destination = /[\s()]/.test(url) ? `<${url}>` : url;
  return `![${alt}](${destination})`;
}

// describeImageUploadFailure 把上传失败映射成可操作的中文提示。
export function describeImageUploadFailure(error: unknown): string {
  if (error instanceof ImageUploadError) {
    let detail = "";
    try {
      const parsed = JSON.parse(error.bodyText) as { error?: { message?: string } } | null;
      detail = parsed?.error?.message ?? "";
    } catch {
      detail = error.bodyText;
    }
    switch (error.status) {
      case 401:
      case 403:
        return `服务端拒绝了图片上传（需要登录或没有权限）${detail ? `：${detail}` : ""}`;
      case 413:
        return `图片超过服务端的大小限制${detail ? `：${detail}` : ""}`;
      case 415:
        return `服务端不支持这种图片格式（仅 png/jpeg/gif/webp）${detail ? `：${detail}` : ""}`;
      case 507:
        return `服务端图片存储配额已满${detail ? `：${detail}` : ""}`;
      default:
        return `图片上传失败（HTTP ${error.status}）${detail ? `：${detail}` : ""}`;
    }
  }
  const message = error instanceof Error ? error.message : String(error);
  return `图片上传失败：网络或服务不可达（${message}）`;
}
