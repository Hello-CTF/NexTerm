#!/usr/bin/env node
import { execFileSync, spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const args = process.argv.slice(2);
const prevTagOnly = args[0] === "--prev-tag";
const tag = (prevTagOnly ? args[1] : args[0]) || process.env.GITHUB_REF_NAME;
if (!tag || !/^v\S+$/.test(tag)) {
  console.error("usage: release-notes.mjs [--prev-tag] <vX.Y.Z release tag>");
  process.exit(1);
}

function git(...args) {
  return execFileSync("git", args, { cwd: ROOT, encoding: "utf8", env: { ...process.env, LC_ALL: "C" } });
}

function previousReleaseTag(tagName) {
  git("rev-parse", "--verify", `${tagName}^{commit}`);
  // Stable tags only: prerelease tags like v1.0.7-rc.1 must not shrink the final release's range.
  const result = spawnSync("git", ["describe", "--tags", "--abbrev=0", "--match", "v*", "--exclude", "*-*", `${tagName}^`], {
    cwd: ROOT,
    encoding: "utf8",
    env: { ...process.env, LC_ALL: "C" },
  });
  if (result.error) throw result.error;
  return result.status === 0 ? result.stdout.trim() : "";
}

const groups = [
  { type: "feat", title: "功能", commits: [] },
  { type: "fix", title: "修复", commits: [] },
  { type: "perf", title: "性能", commits: [] },
];
const excludedTypes = new Set(["docs", "refactor", "test", "ci", "build", "chore"]);
const conventional = /^(\w+)(?:\(([^)]*)\))?(!)?: (.+)$/;

function compareCommits(a, b) {
  const scopeA = a.scope || "";
  const scopeB = b.scope || "";
  if (scopeA !== scopeB) return scopeA < scopeB ? -1 : 1;
  if (a.text !== b.text) return a.text < b.text ? -1 : 1;
  return 0;
}

// Returns the whole BREAKING CHANGE footer (wrapped and multi-paragraph lines
// included), ending at the next footer token such as "Co-authored-by:" or at the
// end of the message, collapsed into a single line.
function breakingFooterText(body) {
  const lines = body.split("\n");
  const start = lines.findIndex((line) => /^BREAKING[ -]CHANGE:/.test(line));
  if (start < 0) return null;
  const collected = [lines[start].replace(/^BREAKING[ -]CHANGE:/, "")];
  for (const line of lines.slice(start + 1)) {
    if (/^(BREAKING[ -]CHANGE|[A-Za-z][A-Za-z-]*): |^[A-Za-z][A-Za-z-]* #/.test(line)) break;
    collected.push(line);
  }
  return collected.join(" ").replace(/\s+/g, " ").trim();
}

function formatCommit(commit) {
  if (!commit.scope) return `- ${commit.text}`;
  return `- **${commit.scope}**: ${commit.text}`;
}

const previous = previousReleaseTag(tag);
if (prevTagOnly) {
  process.stdout.write(`${previous}\n`);
  process.exit(0);
}
if (!previous) console.warn(`release-notes: no previous v* tag before ${tag}; using the full history`);
const range = previous ? `${previous}..${tag}` : tag;
const messages = git("log", "--no-merges", "--format=%B%x00", range)
  .split("\0")
  .map((message) => message.replace(/^\n+/, "").trim())
  .filter((message) => message !== "");

const other = [];
const excluded = [];
const breaking = [];
for (const message of messages) {
  const [subject, ...bodyLines] = message.split("\n");
  const body = bodyLines.join("\n");
  const match = conventional.exec(subject);
  const commit = match
    ? { type: match[1], scope: match[2] || "", text: match[4] }
    : { type: "", scope: "", text: subject };
  const breakingFooter = breakingFooterText(body);
  if ((match && match[3]) || breakingFooter !== null) {
    breaking.push({ scope: commit.scope, text: breakingFooter || commit.text });
  }
  const group = groups.find((item) => item.type === commit.type);
  if (group) group.commits.push(commit);
  else if (match && excludedTypes.has(commit.type)) excluded.push(commit);
  else other.push(commit);
}
const visible = groups.filter((group) => group.commits.length > 0);
const rendered = [...visible, { title: "其他", commits: visible.length === 0 && other.length === 0 ? excluded : other }];
for (const group of rendered) group.commits.sort(compareCommits);
breaking.sort(compareCommits);

const lines = [`### ${tag} 变化`, ""];
if (breaking.length > 0) {
  lines.push("#### 破坏性变更", "");
  for (const commit of breaking) lines.push(formatCommit(commit));
  lines.push("");
}
for (const group of rendered) {
  if (group.commits.length === 0) continue;
  lines.push(`#### ${group.title}`, "");
  for (const commit of group.commits) lines.push(formatCommit(commit));
  lines.push("");
}
process.stdout.write(`${lines.join("\n")}\n`);
