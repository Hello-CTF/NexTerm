import type { ChatItem } from "./conversation";

export function searchableTextOf(item: ChatItem): string {
  switch (item.role) {
    case "user":
    case "assistant":
    case "reasoning":
    case "plan":
      return item.text;
    case "tool":
      return [
        item.name,
        item.display,
        item.summary ?? "",
        item.text ?? "",
        item.subagent?.text ?? "",
        item.subagent?.summary ?? "",
        item.subagent?.error ?? "",
      ].join("\n");
    case "diff":
      return item.path;
    case "confirm":
      return `${item.tool}\n${item.rendered}`;
    case "question":
      return [item.question, ...item.options].join("\n");
    case "outcome":
      return item.text;
  }
}

export function findItemMatches(items: ChatItem[], query: string): number[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [];
  const matches: number[] = [];
  for (let index = 0; index < items.length; index++) {
    if (searchableTextOf(items[index]).toLowerCase().includes(needle)) matches.push(index);
  }
  return matches;
}

export function stepMatch(cursor: number, count: number, direction: 1 | -1): number {
  if (count <= 0) return -1;
  if (cursor < 0) return direction === 1 ? 0 : count - 1;
  return (cursor + direction + count) % count;
}
