import tseslint from "typescript-eslint";

export default tseslint.config(
  // `.buildcheck` 是临时构建校验目录（`vite build --outDir .buildcheck`）。
  // 它不在 dist/ 下，不忽略的话 lint 会连构建产物一起扫，报出上千个假错误 ——
  // 看着像"质量门挂了"，其实只是扫错了文件。
  { ignores: ["dist", ".buildcheck", "src-tauri", "target", "scripts", "*.cjs"] },
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
