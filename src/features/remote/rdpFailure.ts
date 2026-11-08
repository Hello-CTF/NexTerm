import type { EngineFailure } from "./remoteTypes";
import { isMac } from "../../app/platform";
import { t } from "../../i18n";

// A failure as the user reads it: a short title, one actionable next step, and the raw engine text for the details
// fold. The headline never carries engine text, CI paths or backtraces.
export interface FailureText {
  title: string;
  advice: string;
  // Raw engine text, kept for the collapsed details; empty when there is nothing beyond the headline.
  detail: string;
}

// IronErrorKind from @devolutions/iron-remote-desktop (index.d.ts).
export const IRON_GENERAL = 0;
export const IRON_WRONG_PASSWORD = 1;
export const IRON_LOGON_FAILURE = 2;
export const IRON_ACCESS_DENIED = 3;
export const IRON_RDCLEANPATH = 4;
export const IRON_PROXY_CONNECT = 5;
export const IRON_NEGOTIATION = 6;

// Winsock codes the relay sends in RDCleanPath errors (internal/remotedesktop/rdp.go).
const WSA_TIMED_OUT = 10060;
const WSA_CONN_REFUSED = 10061;
const WSA_HOST_UNREACHABLE = 10065;
const WSA_HOST_NOT_FOUND = 11001;

const NSTATUS_PATTERN = /NSTATUS code 0x([0-9a-f]{1,8})/i;
const CJK = /[\u3000-\u9fff\uff00-\uffef]/;
const MAX_DETAIL = 2000;

// Documented logon NTSTATUS values. Each message says what the code means and stops there: the same code can come
// from a wrong user name, so the advice always lists what to check.
const NSTATUS_TEXT: Record<string, { title: string; advice: string }> = {
  C000006D: { title: t("登录被拒绝：用户名或密码不正确"), advice: t("请检查用户名、密码和域是否正确（本地账户的域留空，或填主机名）。") },
  C000006A: { title: t("登录被拒绝：密码不正确"), advice: t("请检查密码是否正确，注意大小写和输入法。") },
  C0000064: { title: t("登录被拒绝：用户名不存在"), advice: t("远程主机找不到该用户。请检查用户名和域是否正确。") },
  C0000071: { title: t("登录被拒绝：密码已过期"), advice: t("请先在远程主机上修改该账户的密码，再重新连接。") },
  C0000224: { title: t("登录被拒绝：需要先修改密码"), advice: t("该账户要求下次登录时修改密码。请在远程主机上修改后再连接。") },
  C0000072: { title: t("登录被拒绝：账户已被禁用"), advice: t("请联系管理员启用该账户，或换用其他账户。") },
  C0000234: { title: t("登录被拒绝：账户已被锁定"), advice: t("多次输错密码可能导致锁定。请等待锁定时间结束或联系管理员解锁，先不要反复重试。") },
  C000006E: { title: t("登录被拒绝：账户受到限制"), advice: t("账户可能被限制登录，例如不允许空密码或不在允许的登录时间内。请联系管理员或检查账户策略。") },
  C000015B: { title: t("登录被拒绝：该账户没有远程登录权限"), advice: t("请把该用户加入远程主机的“Remote Desktop Users”组，或授予“允许通过远程桌面服务登录”。") },
  C0000193: { title: t("登录被拒绝：账户已过期"), advice: t("请联系管理员延长该账户的有效期，或换用其他账户。") },
};

const GENERIC_LOGIN: Pick<FailureText, "title" | "advice"> = {
  title: t("登录被拒绝"),
  advice: t("远程主机在身份验证（CredSSP/NLA）阶段拒绝了登录，原因未给出。请检查用户名、密码和域是否正确（域可留空或填主机名），并确认该账户允许远程桌面登录。"),
};

export function parseNstatus(text: string): string | null {
  const match = NSTATUS_PATTERN.exec(text);
  return match ? match[1].toUpperCase().padStart(8, "0") : null;
}

// Whether the engine text says the failure came from the CredSSP (NLA) exchange.
export function isCredsspText(text: string): boolean {
  return /credssp/i.test(text);
}

// Removes absolute build paths and source locations so engine text can never become a headline by accident.
export function stripBuildPaths(text: string): string {
  return text
    .replace(/\s*@\s*(?:[A-Za-z]:)?[^\s\]:]*[\\/][^\s\]]*(?::\d+)*/g, "")
    .replace(/(?:[A-Za-z]:)?(?:[\\/][\w.@+-]+){2,}(?::\d+){0,2}/g, "")
    .replace(/\s{2,}/g, " ")
    .trim();
}

function clip(text: string): string {
  return text.length > MAX_DETAIL ? `${text.slice(0, MAX_DETAIL)}…` : text;
}

function rawOf(failure: EngineFailure): string {
  const parts = [failure.detail ?? failure.message];
  const extra: string[] = [];
  if (failure.rdpKind !== undefined) extra.push(`kind=${failure.rdpKind}`);
  if (failure.httpStatus !== undefined) extra.push(`http=${failure.httpStatus}`);
  if (failure.tlsAlert !== undefined) extra.push(`tlsAlert=${failure.tlsAlert}`);
  if (failure.wsaError !== undefined) extra.push(`wsa=${failure.wsaError}`);
  if (extra.length > 0) parts.push(`[${extra.join(" ")}]`);
  return clip(parts.filter(Boolean).join("\n"));
}

function withDetail(text: Omit<FailureText, "detail">, failure: EngineFailure): FailureText {
  return { ...text, detail: rawOf(failure) };
}

function relayFailure(failure: EngineFailure): Omit<FailureText, "detail"> | null {
  switch (failure.wsaError) {
    case WSA_CONN_REFUSED:
      return { title: t("远程主机拒绝了连接"), advice: t("主机在线，但该端口没有 RDP 服务在监听。请确认远程桌面已启用，并检查端口号和防火墙。") };
    case WSA_TIMED_OUT:
      return { title: t("连接远程主机超时"), advice: t("没有收到主机的响应。请确认主机已开机、网络可达、端口号正确，且防火墙放行了 RDP。") };
    case WSA_HOST_UNREACHABLE:
      return {
        title: t("无法访问远程主机"),
        advice: isMac()
          ? t("没有到达主机的网络路由。请确认主机地址和网络可达；在 macOS 上还需在「系统设置 > 隐私与安全性 > 本地网络」中允许 NexTerm 访问。")
          : t("没有到达主机的网络路由。请确认主机地址正确、网络可达。"),
      };
    case WSA_HOST_NOT_FOUND:
      return { title: t("找不到远程主机"), advice: t("无法解析主机地址。请检查资产里的主机名或 IP 是否正确。") };
    default:
      return failure.wsaError !== undefined
        ? { title: t("无法连接到远程主机"), advice: t("网络连接失败。请确认主机已开机、地址和端口正确、网络可达。") }
        : null;
  }
}

// describeRdpFailure turns an IronRDP failure into Chinese text. Anything it does not recognise gets a generic
// message; the engine text goes to `detail`.
export function describeRdpFailure(failure: EngineFailure): FailureText {
  const raw = failure.detail ?? failure.message ?? "";
  const clean = stripBuildPaths(failure.message ?? "");

  // CredSSP runs after the TLS upgrade and only fails when the server rejects the login, whatever the error kind.
  if (isCredsspText(raw)) {
    const code = parseNstatus(raw);
    const known = code ? NSTATUS_TEXT[code] : undefined;
    return withDetail(known ?? GENERIC_LOGIN, failure);
  }

  switch (failure.rdpKind) {
    case IRON_WRONG_PASSWORD:
    case IRON_LOGON_FAILURE:
      return withDetail({ title: t("登录被拒绝：用户名或密码不正确"), advice: t("请检查用户名、密码和域是否正确。") }, failure);
    case IRON_ACCESS_DENIED:
      return withDetail({ title: t("远程主机拒绝了访问"), advice: t("账户可能没有远程桌面登录权限。请把用户加入“Remote Desktop Users”组，或联系管理员。") }, failure);
    case IRON_NEGOTIATION:
      return withDetail(
        {
          title: t("无法与远程主机协商安全协议"),
          advice: t("NexTerm 需要 TLS 或 NLA，而主机不接受（例如只允许传统 RDP 安全层，或要求了不同的安全设置）。请在主机的远程桌面设置中启用网络级别身份验证（NLA）。"),
        },
        failure,
      );
    case IRON_PROXY_CONNECT:
      return withDetail({ title: t("无法连接到 NexTerm 中继"), advice: t("本机到中继的连接失败。请检查网络，稍后重新连接。") }, failure);
    case IRON_RDCLEANPATH: {
      if (failure.httpStatus === 403) return withDetail({ title: t("服务器证书需要确认"), advice: t("请信任该主机的证书后重新连接。") }, failure);
      if (failure.httpStatus === 401) return withDetail({ title: t("连接票据已失效"), advice: t("请重新连接。") }, failure);
      const relay = relayFailure(failure);
      if (relay) return withDetail(relay, failure);
      if (failure.tlsAlert !== undefined || /tls/i.test(raw)) {
        return withDetail({ title: t("与远程主机的 TLS 握手失败"), advice: t("主机可能未启用 TLS，或证书、协议版本不被接受。请检查主机的远程桌面安全设置后重试。") }, failure);
      }
      return withDetail(
        { title: t("远程主机在握手阶段中断了连接"), advice: t("可能是 TLS 握手失败、主机没有返回有效应答或在连接建立前关闭了连接。请确认主机的远程桌面可用后重新连接。") },
        failure,
      );
    }
    default:
      break;
  }

  if (/invalid state|this is a bug/i.test(raw)) {
    return withDetail(
      { title: t("RDP 连接在握手阶段失败"), advice: t("客户端和主机在连接初始化时状态不一致，原因未能确定。请重新连接一次；如果反复出现，请复制详情反馈。") },
      failure,
    );
  }

  // A message that is already Chinese (from the app or the relay) is safe to show once paths are gone.
  if (clean && CJK.test(clean) && failure.rdpKind === undefined) {
    return { title: failure.wasConnected ? t("连接已中断") : t("无法连接远程桌面"), advice: clean, detail: clean === (failure.detail ?? failure.message) ? "" : rawOf(failure) };
  }
  return withDetail(
    {
      title: failure.wasConnected ? t("远程桌面连接已中断") : t("RDP 连接失败"),
      advice: t("远程桌面返回了未能识别的错误。请重新连接；如果反复出现，请展开详情并复制反馈。"),
    },
    failure,
  );
}

// describeVncFailure keeps Chinese messages and replaces the English reasons noVNC reports with a generic one.
export function describeVncFailure(failure: EngineFailure): FailureText {
  const clean = stripBuildPaths(failure.message ?? "");
  if (clean && CJK.test(clean)) {
    return { title: failure.wasConnected ? t("连接已中断") : t("无法连接远程桌面"), advice: clean, detail: clean === failure.message ? "" : clip(failure.message) };
  }
  return {
    title: failure.stage === "security" ? t("VNC 安全协商失败") : t("VNC 连接失败"),
    advice: t("VNC 服务器返回了未能识别的信息。请确认服务器的地址、端口和密码，然后重新连接。"),
    detail: clip(failure.message ?? ""),
  };
}
