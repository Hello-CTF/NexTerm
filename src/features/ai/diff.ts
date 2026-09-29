/**
 * 行级 diff 的纯逻辑。
 *
 * 单独成文件而不是留在 `AiSidebar.tsx` 里：确认卡片的「改动预览」和对话流里的
 * 「变更记录」卡片用的是同一份算法，两处各写一份的话，同一个改动会在批准前和
 * 执行后显示成两个样子 —— 用户会以为中间又变了一次。
 *
 * 没有依赖（不 import React），因此可以直接用
 * `node --experimental-strip-types` 跑起来验，不必为了验它先搭一套前端测试框架。
 */

export type DiffLine = { kind: "add" | "del" | "same"; text: string };

/**
 * 切行。两个容易踩的点（都会在卡片上多出一条莫名其妙的空行）：
 *
 * 1. **新建文件的 `before` 是空串**。`"".split("\n")` 得到的是 `[""]`，
 *    也就是"一个空行"，diff 里会先冒出一条 `- `；空串必须是**零行**。
 * 2. 文件以换行结尾时，`split` 会在末尾多切出一个空串 —— 那是文件的结束符，
 *    不是一行内容，不摘掉就在结尾多一条 `+ `。
 */
export function toLines(s: string): string[] {
  if (s === "") return [];
  const lines = s.split("\n");
  if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/**
 * 极简行级 diff：摘掉公共前后缀，中间整段标成删/增。
 *
 * 刻意不做 LCS —— 这里只要"一眼看出改了哪几行"，而 AI 改配置基本都是局部替换，
 * 前后缀一摘就只剩那几行；上完整 diff 算法反而把界面和代码都搞重。
 * 两侧各留 2 行上下文，免得用户看不出改动的落点。
 */
export function simpleDiff(before: string, after: string): DiffLine[] {
  const a = toLines(before);
  const b = toLines(after);

  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head++;

  let tail = 0;
  while (
    tail < a.length - head &&
    tail < b.length - head &&
    a[a.length - 1 - tail] === b[b.length - 1 - tail]
  ) {
    tail++;
  }

  const out: DiffLine[] = [];
  const ctx = 2;
  for (let i = Math.max(0, head - ctx); i < head; i++) out.push({ kind: "same", text: a[i] });
  for (let i = head; i < a.length - tail; i++) out.push({ kind: "del", text: a[i] });
  for (let i = head; i < b.length - tail; i++) out.push({ kind: "add", text: b[i] });
  for (let i = 0; i < Math.min(ctx, tail); i++) {
    out.push({ kind: "same", text: a[a.length - tail + i] });
  }
  return out;
}

/** 卡片上的单行文本：`+ `/`- `/`  ` 前缀。渲染与校验共用，免得两处前缀不一致。 */
export function diffLineText(line: DiffLine): string {
  return (line.kind === "add" ? "+ " : line.kind === "del" ? "- " : "  ") + line.text;
}

/** 这段 diff 里有没有肉眼可辨的增删。 */
export function hasVisibleChange(lines: DiffLine[]): boolean {
  return lines.some((l) => l.kind !== "same");
}
