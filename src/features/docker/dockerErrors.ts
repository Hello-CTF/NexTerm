import { describeError } from "../../ui/errorText";

const DOCKER_UNAVAILABLE_HINTS: { pattern: RegExp; hint: string }[] = [
  {
    pattern: /Cannot connect to the Docker daemon|error during connect|Is the docker daemon running/i,
    hint: "无法连接这台主机上的 Docker 守护进程，请确认 Docker 已安装并运行",
  },
  {
    pattern: /command not found|executable file not found|exit status 127/i,
    hint: "这台主机没有安装 Docker（或不在 PATH 中）",
  },
];

export function describeDockerError(e: unknown): string {
  const raw = describeError(e);
  for (const { pattern, hint } of DOCKER_UNAVAILABLE_HINTS) {
    if (pattern.test(raw)) return `${raw} · ${hint}`;
  }
  return raw;
}
