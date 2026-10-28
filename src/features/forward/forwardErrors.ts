import { describeError } from "../../ui/errorText";

const ADDRESS_IN_USE = /address already in use|EADDRINUSE|only one usage of each socket address|端口.*(占用|被使用)/i;
const PERMISSION_DENIED = /permission denied|EACCES|access is denied/i;

// describeForwardError turns a failed listen into a sentence with the next step; the original text stays in
// brackets so nothing is hidden from the user.
export function describeForwardError(error: unknown, port?: number | string): string {
  const raw = describeError(error);
  const label = port ? `端口 ${port}` : "该端口";
  if (ADDRESS_IN_USE.test(raw)) return `${label} 已被其他程序占用，请更换端口。（${raw}）`;
  if (PERMISSION_DENIED.test(raw)) return `无权使用${label}，请更换为大于 1024 的端口。（${raw}）`;
  return raw;
}
