import { useState, type KeyboardEvent } from "react";
import { fileVisual } from "../../../src/features/files/fileTypes";
import {
  IconArrowUp,
  IconChevronDown,
  IconChevronRight,
  IconFilePlus,
  IconFoldAll,
  IconFolderPlus,
  IconLocate,
  IconRefresh,
  IconSave,
  IconUpload,
} from "../../../src/ui/icons";
import { pick, useToasts, type DemoProps } from "./common";

const ROOT = "/home/deploy/apps/api-server";

const copy = {
  zh: {
    tree: "远程文件", newFile: "新建文件", newDir: "新建目录", refresh: "刷新文件列表", foldAll: "折叠全部目录", upload: "上传到当前目录",
    follow: "跟随终端目录", followOn: "跟随终端目录：开。终端切换目录时，文件树自动跟随", followOff: "跟随终端目录：关。单击可开启自动跟随",
    up: "上级目录 /home/deploy/apps", unsaved: "未保存", save: "保存", saved: (path: string) => `已保存 ${path}`,
    openHint: "双击或按 Enter 打开文件", editor: "编辑器",
  },
  en: {
    tree: "Remote files", newFile: "New file", newDir: "New folder", refresh: "Refresh file list", foldAll: "Collapse all folders", upload: "Upload to this folder",
    follow: "Follow terminal directory", followOn: "Follow terminal directory: on. The tree follows the terminal when it changes directory", followOff: "Follow terminal directory: off. Click to follow automatically",
    up: "Parent folder /home/deploy/apps", unsaved: "Unsaved", save: "Save", saved: (path: string) => `Saved ${path}`,
    openHint: "Double-click or press Enter to open a file", editor: "Editor",
  },
};

const initialFiles: Record<string, string> = {
  "docker-compose.yml": [
    "services:", "  api:", "    image: nexterm/api:2.4", "    restart: unless-stopped", "    ports:", "      - \"127.0.0.1:8080:8080\"",
    "    environment:", "      DATABASE_URL: postgres://postgres:5432/app", "      LOG_LEVEL: info", "    healthcheck:",
    "      test: [\"CMD\", \"curl\", \"-f\", \"http://localhost:8080/healthz\"]", "      interval: 30s", "",
    "  postgres:", "    image: postgres:16", "    volumes:", "      - postgres-data:/var/lib/postgresql/data", "",
    "volumes:", "  postgres-data:", "",
  ].join("\n"),
  "nginx.conf": [
    "upstream api_backend {", "    server 127.0.0.1:8080;", "    keepalive 32;", "}", "",
    "server {", "    listen 443 ssl;", "    server_name api.example.test;", "",
    "    location /api/ {", "        proxy_pass http://api_backend;", "        proxy_connect_timeout 5s;", "        proxy_read_timeout 30s;", "    }", "",
    "    location /healthz {", "        proxy_pass http://api_backend;", "    }", "}", "",
  ].join("\n"),
  "README.md": ["# API service", "", "Example project used by the NexTerm demo.", "No files or hosts shown here are real.", "", "## Deploy", "", "1. Review docker-compose.yml", "2. Run scripts/deploy.sh", ""].join("\n"),
  "scripts/deploy.sh": ["#!/usr/bin/env bash", "set -euo pipefail", "", "docker compose pull api", "docker compose up -d api", "curl -sf localhost:8080/healthz", ""].join("\n"),
};

interface Entry {
  path: string;
  name: string;
  dir?: boolean;
  depth: number;
}

const entries: Entry[] = [
  { path: "scripts", name: "scripts", dir: true, depth: 0 },
  { path: "scripts/deploy.sh", name: "deploy.sh", depth: 1 },
  { path: "README.md", name: "README.md", depth: 0 },
  { path: "docker-compose.yml", name: "docker-compose.yml", depth: 0 },
  { path: "nginx.conf", name: "nginx.conf", depth: 0 },
];

type Token = [string, string | null];

// highlight colors the sample files; it covers only the syntax they use.
function highlight(file: string, line: string): Token[] {
  if (file.endsWith(".md")) return /^#/.test(line) ? [[line, "tok-heading"]] : [[line, null]];
  const comment = line.indexOf("#");
  if (comment >= 0 && !/["']/.test(line.slice(0, comment))) return [...highlight(file, line.slice(0, comment)), [line.slice(comment), "tok-comment"]];
  if (file.endsWith(".yml")) {
    const key = /^(\s*-?\s*)([\w.-]+)(:)(.*)$/.exec(line);
    if (key) return [[key[1], null], [key[2], "tok-key"], [key[3], "tok-punct"], ...values(key[4])];
    return values(line);
  }
  if (file.endsWith(".sh")) {
    const command = /^(\s*)([\w-]+)(.*)$/.exec(line);
    if (command) return [[command[1], null], [command[2], "tok-fn"], ...values(command[3])];
    return values(line);
  }
  const directive = /^(\s*)([\w_]+)(.*)$/.exec(line);
  if (directive) return [[directive[1], null], [directive[2], "tok-key"], ...values(directive[3])];
  return [[line, "tok-punct"]];
}

function values(text: string): Token[] {
  return text.split(/("[^"]*"|\b\d+(?:\.\d+)*[a-z]*\b|[{}[\];,])/).filter(Boolean).map((part) => [
    part,
    part.startsWith("\"") ? "tok-string" : /^\d/.test(part) ? "tok-number" : /^[{}[\];,]$/.test(part) ? "tok-punct" : null,
  ]);
}

export default function FilesDemo({ language }: DemoProps) {
  const t = pick(language, copy);
  const { push: pushToast, view: toastView } = useToasts(language);
  const [saved, setSaved] = useState(initialFiles);
  const [drafts, setDrafts] = useState(initialFiles);
  const [open, setOpen] = useState("nginx.conf");
  const [selected, setSelected] = useState("nginx.conf");
  const [expanded, setExpanded] = useState(true);
  const [follow, setFollow] = useState(true);

  const text = drafts[open];
  const dirty = text !== saved[open];
  const lines = text.split("\n");
  const fullPath = `${ROOT}/${open}`;
  const { Icon: OpenIcon, tone: openTone } = fileVisual(open, "file");

  const openFile = (path: string) => {
    setSelected(path);
    setOpen(path);
  };
  const save = () => {
    if (!dirty) return;
    setSaved((current) => ({ ...current, [open]: text }));
    pushToast("success", t.saved(fullPath));
  };
  const onEditorKey = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
      event.preventDefault();
      save();
    }
  };
  const visible = entries.filter((entry) => entry.depth === 0 || expanded);
  const mod = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform) ? "⌘" : "Ctrl";

  return <div className="nx-pane demo-files">
    <div className="demo-files-body">
      <nav className="demo-files-tree" aria-label={t.tree}>
        <div className="nx-toolbar demo-files-toolbar">
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.newFile} aria-label={t.newFile}><IconFilePlus size={14} /></button>
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.newDir} aria-label={t.newDir}><IconFolderPlus size={14} /></button>
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.refresh} aria-label={t.refresh}><IconRefresh size={14} /></button>
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.foldAll} aria-label={t.foldAll} onClick={() => setExpanded(false)}><IconFoldAll size={14} /></button>
          <button type="button" className={`nx-icon-btn nx-icon-btn-sm${follow ? " is-active" : ""}`} title={follow ? t.followOn : t.followOff} aria-label={t.follow} aria-pressed={follow} onClick={() => setFollow((on) => !on)}><IconLocate size={14} /></button>
          <span className="nx-spacer" />
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.upload} aria-label={t.upload}><IconUpload size={14} /></button>
        </div>
        <div className="nx-pathbar">
          <button type="button" className="nx-tree-caret" title={t.up} aria-label={t.up}><IconArrowUp size={12} /></button>
          <span className="demo-path">{ROOT}</span>
        </div>
        <div className="demo-tree-rows" title={t.openHint}>
          {visible.map((entry) => {
            const { Icon, tone } = fileVisual(entry.name, entry.dir ? "dir" : "file");
            const isSelected = selected === entry.path;
            return <div
              key={entry.path}
              role="button"
              tabIndex={0}
              aria-expanded={entry.dir ? expanded : undefined}
              aria-current={open === entry.path ? "true" : undefined}
              className={`nx-row demo-tree-row${isSelected ? " is-selected" : ""}`}
              style={{ paddingLeft: 4 + entry.depth * 12 }}
              onClick={() => {
                setSelected(entry.path);
                if (entry.dir) setExpanded((value) => !value);
              }}
              onDoubleClick={() => { if (!entry.dir) openFile(entry.path); }}
              onKeyDown={(event) => {
                if (event.key === "Enter" || event.key === " ") {
                  event.preventDefault();
                  if (entry.dir) setExpanded((value) => !value);
                  else if (event.key === "Enter") openFile(entry.path);
                  else setSelected(entry.path);
                }
              }}
            >
              <span className="demo-tree-caret">{entry.dir ? (expanded ? <IconChevronDown size={11} /> : <IconChevronRight size={11} />) : null}</span>
              <Icon size={14} className={tone} />
              <span className="nx-row-name">{entry.name}</span>
              {drafts[entry.path] !== undefined && drafts[entry.path] !== saved[entry.path] && <span className="demo-dirty-dot" role="img" aria-label={t.unsaved} />}
            </div>;
          })}
        </div>
      </nav>
      <section className="demo-editor" aria-label={t.editor}>
        <div className="nx-toolbar">
          <OpenIcon size={14} className={openTone} />
          <span className="nx-toolbar-title demo-mono demo-truncate">{fullPath}</span>
          {dirty && <span className="nx-badge nx-badge-amber"><span className="nx-dot" />{t.unsaved}</span>}
          <span className="nx-spacer" />
          <button type="button" className={`nx-btn nx-btn-sm ${dirty ? "nx-btn-primary" : "nx-btn-ghost"}`} disabled={!dirty} onClick={save} title={t.save}>
            <IconSave size={13} />{t.save}<span className="nx-kbd demo-hide-narrow">{mod} S</span>
          </button>
        </div>
        <div className="demo-editor-meta"><span>{(new TextEncoder().encode(text).byteLength / 1024).toFixed(1)} KB</span><span>|</span><span>UTF-8</span><span>|</span><span>LF</span></div>
        <div className="demo-code">
          <div className="demo-code-gutter" aria-hidden="true">{lines.map((_, index) => <div key={index}>{index + 1}</div>)}</div>
          <div className="demo-code-area">
            <pre aria-hidden="true">{lines.map((line, index) => <div key={index}>{line ? highlight(open, line).map(([part, kind], n) => kind ? <span key={n} className={kind}>{part}</span> : part) : " "}</div>)}</pre>
            <textarea
              aria-label={fullPath}
              value={text}
              spellCheck={false}
              wrap="off"
              rows={lines.length}
              style={{ width: `max(100%, ${Math.max(...lines.map((line) => line.length)) + 4}ch)` }}
              onChange={(event) => setDrafts((current) => ({ ...current, [open]: event.target.value }))}
              onKeyDown={onEditorKey}
            />
          </div>
        </div>
      </section>
    </div>
    {toastView}
  </div>;
}
