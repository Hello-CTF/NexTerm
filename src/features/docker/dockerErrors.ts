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

// Codes the backend uses when the host itself cannot run Docker; their
// messages are already user-facing.
const DOCKER_HOST_CODES = new Set(["docker_not_installed", "docker_unavailable", "docker_forbidden"]);

function dockerErrorCode(e: unknown): string {
  return e && typeof e === "object" && typeof (e as { code?: unknown }).code === "string" ? (e as { code: string }).code : "";
}

export function isDockerNotInstalled(e: unknown): boolean {
  return dockerErrorCode(e) === "docker_not_installed";
}

// dockerHostUnavailableText explains a host without a usable Docker (not installed or the daemon not running), or
// returns null for any other error.
export function dockerHostUnavailableText(e: unknown): string | null {
  const code = dockerErrorCode(e);
  if (code === "docker_not_installed") return "这台主机没有安装 Docker，安装后点击重试。";
  if (code === "docker_unavailable") return "这台主机未运行 Docker，启动后点击重试。";
  return null;
}

export function describeDockerError(e: unknown): string {
  if (DOCKER_HOST_CODES.has(dockerErrorCode(e))) {
    const message = (e as { message?: unknown }).message;
    if (typeof message === "string" && message) return message;
  }
  const raw = describeError(e);
  for (const { pattern, hint } of DOCKER_UNAVAILABLE_HINTS) {
    if (pattern.test(raw)) return `${raw} · ${hint}`;
  }
  return raw;
}
