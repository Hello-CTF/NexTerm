import tseslint from "typescript-eslint";

export default tseslint.config(
  // `.buildcheck` 是临时构建校验目录（`vite build --outDir .buildcheck`）。
  // `.sitetest` 是站点校验产物（在 .gitignore 里，但忽略清单是独立的 —— 这个坑踩过两次）。
  // 两者都不在 dist/ 下，不忽略的话 lint 会连构建产物一起扫，报出上千个假错误 ——
  // 看着像"质量门挂了"，其实只是扫错了文件。
  //
  // `lazycat` 同理，而且是**必然会踩**的：`lazycat/image/build-server.sh` 会把
  // `dist/` 整个拷到 `lazycat/content/web/`（那是 LPK 的 contentdir）。
  // 只要打过一次 LPK，那里就躺着一份压缩后的 bundle，`pnpm lint` 立刻从 0 个错误
  // 变成 4000+ 个（报错列号是 600+ 这种，一眼能认出是压缩产物）。
  //
  // `.tower/worktrees` 里是独立检出，源码由各自 worktree 的质量门检查；
  // 根 lint 不应再递归扫描其中的源码、依赖或 dist/build 产物。
  { ignores: ["dist", "cmd/nexterm-desktop/dist", ".buildcheck", ".sitetest", ".tower/worktrees", "lazycat", "src-tauri", "target", "scripts", "*.cjs", "src/ipc/bindings"] },
  tseslint.configs.recommended,
  {
    rules: {
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/consistent-type-imports": [
        "error",
        { fixStyle: "inline-type-imports", disallowTypeAnnotations: false },
      ],
      "no-console": ["error", { allow: ["warn", "error"] }],
    },
  },
);
