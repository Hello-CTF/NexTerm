export type DockerDiffLine = { kind: "add" | "del" | "same"; text: string };

function lines(value: string): string[] {
  return value === "" ? [] : value.split("\n");
}

function redactEnvLine(line: string): string {
  if (line === "") return line;
  const match = /^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=/.exec(line);
  return match ? `${match[1]}（值已脱敏）` : "（非键值内容已脱敏）";
}

export function dockerTextDiff(before: string, after: string, redactEnv = false): DockerDiffLine[] {
  const left = lines(before);
  const right = lines(after);
  let head = 0;
  while (head < left.length && head < right.length && left[head] === right[head]) head++;
  if (head === left.length && head === right.length) return [];
  let tail = 0;
  while (tail < left.length - head && tail < right.length - head && left[left.length - 1 - tail] === right[right.length - 1 - tail]) tail++;
  const result: DockerDiffLine[] = [];
  for (let index = Math.max(0, head - 2); index < head; index++) result.push({ kind: "same", text: left[index] });
  for (let index = head; index < left.length - tail; index++) result.push({ kind: "del", text: left[index] });
  for (let index = head; index < right.length - tail; index++) result.push({ kind: "add", text: right[index] });
  for (let index = Math.max(head, left.length - tail); index < Math.min(left.length, left.length - tail + 2); index++) {
    result.push({ kind: "same", text: left[index] });
  }
  if (redactEnv) {
    for (let index = 0; index < result.length; index++) result[index].text = redactEnvLine(result[index].text);
  }
  return result;
}
