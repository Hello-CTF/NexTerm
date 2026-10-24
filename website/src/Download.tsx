import { useEffect, useState } from "react";
import { Apple, Check, Copy, Download, ExternalLink, Fingerprint, Laptop, Monitor, Server, Smartphone, type LucideIcon } from "lucide-react";

const repository = "https://github.com/Hello-CTF/NexTerm";
const latestRelease = `${repository}/releases/latest`;
const releasesPage = `${repository}/releases`;
const installCommand = "curl -fsSL https://nexterm.work/install.sh | sudo bash";
const testFlightLink = "https://testflight.apple.com/join/ds62pzPz";

type Language = "zh" | "en";
type Arch = "amd64" | "arm64";
type PlatformId = "windows" | "macos" | "linux" | "server" | "android" | "ios" | "lazycat";

interface Platform {
  id: PlatformId;
  icon: LucideIcon;
  archs: Arch[];
  // asset returns the release file name for this platform, or null when it ships elsewhere.
  asset: ((arch: Arch, version: string) => string) | null;
}

const platforms: Platform[] = [
  { id: "windows", icon: Monitor, archs: ["amd64", "arm64"], asset: (arch, version) => `nexterm-desktop_windows_${arch}_${version}.exe` },
  { id: "macos", icon: Apple, archs: ["arm64", "amd64"], asset: (arch, version) => `nexterm-desktop_macos_${arch}_${version}.dmg` },
  { id: "linux", icon: Laptop, archs: ["amd64", "arm64"], asset: (arch, version) => `nexterm-desktop_linux_${arch}_${version}.deb` },
  { id: "server", icon: Server, archs: ["amd64", "arm64"], asset: (arch, version) => `nexterm-server_linux_${arch}_${version}.tar.gz` },
  { id: "android", icon: Smartphone, archs: ["arm64"], asset: (_arch, version) => `nexterm-desktop_android_arm64_${version}.apk` },
  { id: "ios", icon: Smartphone, archs: [], asset: null },
  { id: "lazycat", icon: Fingerprint, archs: [], asset: null },
];

const text = {
  zh: {
    names: { windows: "Windows", macos: "macOS", linux: "Linux 桌面", server: "Linux 服务端", android: "Android", ios: "iOS / iPadOS", lazycat: "懒猫微服" },
    archNames: { amd64: "x64", arm64: "ARM64" }, macArchNames: { amd64: "Intel", arm64: "Apple Silicon" },
    detected: "已识别你的设备", choose: "选择其他系统",
    serverGuide: [
      ["安装", "创建 nexterm 用户与目录，生成 master key，创建超管 admin 并在安装输出中打印一次初始密码（请立即记下，首次登录需修改），然后启动服务。"],
      ["访问", "默认只监听 127.0.0.1:8080。在你的电脑上执行 ssh -L 8080:127.0.0.1:8080 用户@服务器，再打开 http://localhost:8080；公网访问请前置带 TLS 的反向代理。"],
      ["配置", "/etc/nexterm/nexterm.env，修改后执行 systemctl restart nexterm-server。默认 SQLite，也可切换 PostgreSQL / MySQL / MariaDB。请备份 /var/lib/nexterm 和 /etc/nexterm/master.key。"],
      ["升级", "再次执行同一条命令即可升级，服务会短暂重启，配置和数据都会保留；需要撤销时加 --rollback。"],
    ],
    download: "下载", testflight: "加入 TestFlight 测试", version: "最新版本", loading: "正在读取最新版本…", unavailable: "无法读取最新版本，请前往 Releases 下载。",
    copy: "复制", copied: "已复制",
    notes: {
      windows: "运行安装器即可。安装包没有代码签名，提示未知发布者时选择「更多信息 → 仍要运行」。",
      macos: "打开后拖入「应用程序」。浏览器无法识别 Mac 芯片，Intel 机型请点上方的「Intel」。",
      linux: "下载后执行：",
      server: "在带 systemd 的 Linux 服务器（amd64 / arm64）上执行（需要 root 权限）：",
      android: "预览版，适用于 arm64 设备。",
      ios: "预览版通过 TestFlight 安装：在 iPhone 或 iPad 上打开下面的链接，按提示安装 TestFlight 后加入测试。",
      lazycat: "在懒猫微服的应用中心搜索 NexTerm 安装，目前仅支持 amd64 机型。",
    },
  },
  en: {
    names: { windows: "Windows", macos: "macOS", linux: "Linux desktop", server: "Linux server", android: "Android", ios: "iOS / iPadOS", lazycat: "LazyCat" },
    archNames: { amd64: "x64", arm64: "ARM64" }, macArchNames: { amd64: "Intel", arm64: "Apple Silicon" },
    detected: "Detected your device", choose: "Choose another system",
    serverGuide: [
      ["Install", "Creates the nexterm user and directories, generates the master key, creates the admin super-admin and prints its initial password once in the installer output (write it down and change it at first login), then starts the service."],
      ["Access", "Listens on 127.0.0.1:8080 by default. On your computer run ssh -L 8080:127.0.0.1:8080 user@server and open http://localhost:8080; for public access, put a TLS reverse proxy in front."],
      ["Configure", "/etc/nexterm/nexterm.env, then systemctl restart nexterm-server. SQLite by default; PostgreSQL, MySQL, and MariaDB are supported. Back up /var/lib/nexterm and /etc/nexterm/master.key."],
      ["Upgrade", "Run the same command again; the service restarts briefly and configuration and data are kept. Add --rollback to undo the last upgrade."],
    ],
    download: "Download", testflight: "Join the TestFlight beta", version: "Latest version", loading: "Loading the latest version…", unavailable: "The latest version could not be loaded; download it from Releases.",
    copy: "Copy", copied: "Copied",
    notes: {
      windows: "Run the installer. It is not code-signed; if Windows reports an unknown publisher, choose More info → Run anyway.",
      macos: "Open it and drag NexTerm into Applications. Browsers cannot tell Mac chips apart, so on an Intel Mac pick Intel above.",
      linux: "After downloading, run:",
      server: "On a Linux server with systemd (amd64 / arm64), run with root privileges:",
      android: "Preview build for arm64 devices.",
      ios: "The preview installs through TestFlight: open the link below on your iPhone or iPad and follow the prompts.",
      lazycat: "Search for NexTerm in the LazyCat app center; amd64 devices only for now.",
    },
  },
};

interface Detection { platform: PlatformId; arch: Arch }

// detectDevice reads the platform from the user agent. Macs default to Apple
// Silicon and every other platform to amd64, the most common build; visitors
// on another architecture pick it below.
async function detectDevice(): Promise<Detection> {
  const ua = navigator.userAgent;
  let platform: PlatformId = "windows";
  if (/Android/i.test(ua)) platform = "android";
  else if (/iPhone|iPad|iPod/i.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1)) platform = "ios";
  else if (/Windows/i.test(ua)) platform = "windows";
  else if (/Macintosh|Mac OS X/i.test(ua)) platform = "macos";
  else if (/Linux|X11|CrOS/i.test(ua)) platform = "linux";
  return { platform, arch: platform === "macos" ? "arm64" : "amd64" };
}

interface Release { version: string; assets: Map<string, string> }

async function fetchLatestRelease(): Promise<Release> {
  const response = await fetch("https://api.github.com/repos/Hello-CTF/NexTerm/releases/latest", { headers: { Accept: "application/vnd.github+json" } });
  if (!response.ok) throw new Error(`GitHub API ${response.status}`);
  const body = await response.json() as { tag_name: string; assets: { name: string; browser_download_url: string }[] };
  return { version: body.tag_name.replace(/^v/, ""), assets: new Map(body.assets.map((asset) => [asset.name, asset.browser_download_url])) };
}

function CopyCommand({ command, language }: { command: string; language: Language }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    } catch {
      setCopied(false);
    }
  };
  return (
    <div className="command">
      <code>{command}</code>
      <button type="button" className="command-copy" onClick={() => void copy()} aria-label={text[language].copy}>
        {copied ? <Check size={15} /> : <Copy size={15} />}
        <span>{copied ? text[language].copied : text[language].copy}</span>
      </button>
    </div>
  );
}

export function DownloadPicker({ language }: { language: Language }) {
  const t = text[language];
  const [detected, setDetected] = useState<Detection | null>(null);
  const [selected, setSelected] = useState<PlatformId>("windows");
  const [arch, setArch] = useState<Arch>("amd64");
  const [release, setRelease] = useState<Release | null>(null);
  const [releaseError, setReleaseError] = useState(false);

  useEffect(() => {
    let active = true;
    void detectDevice().then((device) => {
      if (!active) return;
      setDetected(device);
      setSelected(device.platform);
      setArch(device.arch);
    });
    fetchLatestRelease().then((latest) => active && setRelease(latest)).catch(() => active && setReleaseError(true));
    return () => { active = false; };
  }, []);

  const platform = platforms.find((item) => item.id === selected) ?? platforms[0];
  const effectiveArch = platform.archs.includes(arch) ? arch : platform.archs[0];
  const assetName = platform.asset && effectiveArch && release ? platform.asset(effectiveArch, release.version) : null;
  const assetUrl = assetName ? release?.assets.get(assetName) : undefined;
  const PlatformIcon = platform.icon;
  const archNames = platform.id === "macos" ? t.macArchNames : t.archNames;

  return (
    <div className="download-picker">
      <div className="download-tabs" role="tablist" aria-label={t.choose}>
        {platforms.map((item) => {
          const Icon = item.icon;
          return (
            <button key={item.id} type="button" role="tab" aria-selected={item.id === selected}
              className={`download-tab ${item.id === selected ? "is-active" : ""}`} onClick={() => setSelected(item.id)}>
              <Icon size={16} />
              <span>{t.names[item.id]}</span>
              {detected?.platform === item.id && <span className="download-detected" title={t.detected} />}
            </button>
          );
        })}
        <a className="download-tab" href={releasesPage} target="_blank" rel="noreferrer">
          <ExternalLink size={16} />
          <span>Releases</span>
        </a>
      </div>

      <div className="download-panel" role="tabpanel">
        <div className="download-head">
          <PlatformIcon size={22} />
          <div>
            <strong>{t.names[platform.id]}</strong>
            <span>
              {release ? `${t.version} v${release.version}` : releaseError ? t.unavailable : t.loading}
            </span>
          </div>
        </div>

        {platform.archs.length > 1 && (
          <div className="download-archs" role="group">
            {platform.archs.map((value) => (
              <button key={value} type="button" className={`download-arch ${value === effectiveArch ? "is-active" : ""}`}
                aria-pressed={value === effectiveArch} onClick={() => setArch(value)}>
                {archNames[value]}
              </button>
            ))}
          </div>
        )}

        <p className="download-note">{t.notes[platform.id]}</p>
        {platform.id === "server" && <CopyCommand command={installCommand} language={language} />}
        {platform.id === "server" && (
          <dl className="download-steps">
            {t.serverGuide.map(([term, description]) => (
              <div key={term}><dt>{term}</dt><dd>{description}</dd></div>
            ))}
          </dl>
        )}
        {platform.id === "linux" && <CopyCommand command={`sudo apt install ./${assetName ?? `nexterm-desktop_linux_${effectiveArch}_<version>.deb`}`} language={language} />}

        {platform.asset && (
          <div className="download-actions">
            <a className="button button-primary" href={assetUrl ?? latestRelease} target={assetUrl ? undefined : "_blank"} rel="noreferrer">
              <Download size={17} />{t.download} {t.names[platform.id]}{effectiveArch && platform.archs.length > 1 ? ` · ${archNames[effectiveArch]}` : ""}
            </a>
          </div>
        )}
        {platform.id === "ios" && (
          <div className="download-actions">
            <a className="button button-primary" href={testFlightLink} target="_blank" rel="noreferrer">
              <Download size={17} />{t.testflight}
            </a>
          </div>
        )}
        {assetName && <p className="download-file">{assetName}</p>}
      </div>
    </div>
  );
}
