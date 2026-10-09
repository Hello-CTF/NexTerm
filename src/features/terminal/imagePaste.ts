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

// markdownFileLink 生成插入终端光标的 Markdown 链接(普通链接, 非图片嵌入)。
// 文本转义反斜杠与方括号; 目标含空白或圆括号时用尖括号包裹(CommonMark 合法目标)。
export function markdownFileLink(name: string, url: string): string {
  const text = (name || "file").replace(/([\\\[\]])/g, "\\$1");
  const destination = /[\s()]/.test(url) ? `<${url}>` : url;
  return `[${text}](${destination})`;
}

// pasteFileExtension 从文件名或 MIME 提取安全的小写扩展名(不含点), 缺省 png。
export function pasteFileExtension(file: File): string {
  const fromName = /\.([A-Za-z0-9]{1,8})$/.exec(file.name)?.[1];
  if (fromName) return fromName.toLowerCase();
  const fromType = /^image\/([A-Za-z0-9]{1,8})$/.exec(file.type)?.[1];
  return fromType ? fromType.toLowerCase() : "png";
}
