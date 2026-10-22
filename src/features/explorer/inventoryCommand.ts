// Parse quoting and escapes only; execution remains an argv call without shell expansion.
export function parseInventoryCommand(input: string): string[] {
  const args: string[] = [];
  let word = "";
  let present = false;
  let quote: "'" | '"' | null = null;
  for (let index = 0; index < input.length; index++) {
    const char = input[index];
    if (quote === "'") {
      if (char === "'") quote = null;
      else word += char;
    } else if (char === "\\") {
      const next = input[index + 1];
      if (next === undefined) throw new Error("命令末尾的转义符不完整");
      if (next === "\n") {
        index++;
        continue;
      }
      if (quote === '"' && !['"', "\\", "$", "`"].includes(next)) {
        word += char;
      } else {
        word += next;
        index++;
      }
      present = true;
    } else if (quote === '"') {
      if (char === '"') quote = null;
      else word += char;
    } else if (char === "'" || char === '"') {
      quote = char;
      present = true;
    } else if (/\s/.test(char)) {
      if (present) args.push(word);
      word = "";
      present = false;
    } else {
      word += char;
      present = true;
    }
  }
  if (quote !== null) throw new Error("命令中的引号未闭合");
  if (present) args.push(word);
  return args;
}

export function formatInventoryCommand(args: string[]): string {
  return args.map((arg) => /^[a-zA-Z0-9_./:@%+=,-]+$/.test(arg) ? arg : "'" + arg.replaceAll("'", "'\\''") + "'").join(" ");
}
