#!/usr/bin/env python3
"""NexTerm 应用商店截图合成器。

把「真实窗口截取」与「应用内演示截图」统一成同一套版式的商店图：
深色画布 + 顶部标题栏 + 居中带圆角描边的界面卡片。

为什么要它，而不是直接传原图：
  1. 来源尺寸不一（1680x1050 的演示图 / 1480x920 的真实窗口），
     直接传会让商店轮播图长短不齐；统一版式后视觉尺寸一致。
  2. 截图里可能带本机域名、内网 IP 之类不该公开的东西，
     打码规则集中在 REDACT 表里，不会漏（每张都过一遍）。
  3. 上架审核打回「截图规格不符」时，改一个参数就能整体重出。

用法::

    python3 scripts/store-shots.py                    # 输出到 docs/store/（1920x1080 / 16:9）
    python3 scripts/store-shots.py --size 1600x900    # 换画布尺寸（**保持 16:9 才合规**）
    python3 scripts/store-shots.py --verify           # 另出打码核对图

原始窗口截图放 docs/store/raw/（**该目录已 gitignore**，因为里面必然带
本机域名；合成后的产物是打过码的，才进版本库）。
缺失 raw 的条目会被跳过并给出提示，不会中断整批。

依赖：Pillow（本机用 /Users/macmini/.workbuddy/binaries/python/envs/default/bin/python）
"""

from __future__ import annotations

import argparse
import os
import sys
from dataclasses import dataclass, field

from PIL import Image, ImageDraw, ImageFilter, ImageFont, ImageOps

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# ---------------------------------------------------------------- 打码规则
# 坐标是**源图**像素。每张图独立列出，避免「假设布局一致」这种错。
# 打码方式：对区域做重高斯模糊 + 轻微压暗，看不出原文即可。
REDACT: dict[str, list[tuple[int, int, int, int]]] = {
    # 演示图里终端首行 "Last login: ... from 10.0.0.8"（内网 IP）。
    # 该行实测占 y 197..209（下一行 "# 演示模式…" 从 211 起）⇒ 框只压这一行，
    # 不越界切到下一条（切了会留下半截字，反而显眼）。
    "docs/images/hero-ai-terminal.png": [(288, 194, 692, 210)],
    "docs/images/ai-permission.png": [(288, 194, 692, 210)],
    "docs/images/ai-file-changes.png": [(288, 194, 692, 210)],
    "docs/images/split-pane-editor.png": [(288, 194, 692, 210)],
    # 真实窗口截图：资产树里的私有盒子域名 / 资产名。
    # 域名串实测右端止于 x≈283 ⇒ 右边界留到 300 整行吃掉；再往右是设置面板，不能碰。
    "docs/store/raw/terminal.png": [(56, 164, 300, 194)],
    "docs/store/raw/settings-ai.png": [(56, 166, 300, 196)],
}

# ---------------------------------------------------------------- 出图清单
@dataclass
class Shot:
    out: str
    src: str
    title: str
    sub: str = ""
    redact: list[tuple[int, int, int, int]] = field(default_factory=list)


SHOTS: list[Shot] = [
    Shot("01-workspace-overview.png", "docs/images/hero-ai-terminal.png",
         "工作区总览", "终端、文件、容器、数据库、AI 助手，一个窗口收束"),
    Shot("02-live-terminal.png", "docs/store/raw/terminal.png",
         "真实终端会话", "本机与远程统一入口，连接状态实时可见"),
    Shot("03-ai-guardrail.png", "docs/images/ai-permission.png",
         "AI 权限护栏", "命令按风险分级，危险操作先确认再执行"),
    Shot("04-ai-file-diff.png", "docs/images/ai-file-changes.png",
         "AI 改文件留痕", "写入前给出前后对照，改动可逐行核对"),
    Shot("05-split-workspace.png", "docs/images/split-pane-editor.png",
         "分屏工作区", "终端与文件编辑器同屏，边看边改"),
    Shot("06-port-forward.png", "docs/images/port-forward.png",
         "端口转发", "SSH 隧道一键转发到本机，浏览器直接访问内网服务"),
    Shot("07-ai-models.png", "docs/store/raw/settings-ai.png",
         "多模型接入", "自带密钥（BYOK），DeepSeek / 智谱 / OpenAI 兼容端点随选"),
]

# ---------------------------------------------------------------- 视觉常量
BRAND = (0x2E, 0x6B, 0xE6)
BG_TOP = (0x0B, 0x0E, 0x14)
BG_BOTTOM = (0x15, 0x1A, 0x27)
TITLE_COLOR = (0xEA, 0xEE, 0xF5)
SUB_COLOR = (0x8B, 0x94, 0xA7)
CARD_BORDER = (0xFF, 0xFF, 0xFF, 34)

FONT_CANDIDATES = [
    ("/System/Library/Fonts/Hiragino Sans GB.ttc", 0),
    ("/System/Library/Fonts/STHeiti Medium.ttc", 0),
    ("/System/Library/Fonts/Supplemental/Songti.ttc", 0),
    ("/System/Library/Fonts/Supplemental/Arial Unicode.ttf", 0),
]


def load_font(size: int) -> ImageFont.FreeTypeFont:
    for path, idx in FONT_CANDIDATES:
        if os.path.exists(path):
            try:
                return ImageFont.truetype(path, size, index=idx)
            except OSError:
                continue
    raise SystemExit("找不到可用的中文字体")


def vertical_gradient(size: tuple[int, int], top, bottom) -> Image.Image:
    w, h = size
    grad = Image.new("RGB", (1, h))
    px = grad.load()
    for y in range(h):
        t = y / max(h - 1, 1)
        px[0, y] = tuple(int(top[i] + (bottom[i] - top[i]) * t) for i in range(3))
    return grad.resize((w, h), Image.BILINEAR)


def add_glow(base: Image.Image, size: tuple[int, int]) -> Image.Image:
    w, h = size
    g = ImageOps.invert(Image.radial_gradient("L").resize((int(w * 0.95), int(h * 0.9))))
    glow = Image.new("RGBA", size, BRAND + (0,))
    layer = Image.new("L", size, 0)
    layer.paste(g, ((w - g.width) // 2, -int(h * 0.22)))
    glow.putalpha(layer.point(lambda v: int(v * 0.13)))
    return Image.alpha_composite(base, glow)


def apply_redactions(im: Image.Image, boxes) -> Image.Image:
    """重模糊 + 压暗。模糊半径与框宽成比例，保证细文字彻底糊掉。"""
    if not boxes:
        return im
    out = im.convert("RGB")
    for (x0, y0, x1, y1) in boxes:
        x0, y0 = max(0, x0), max(0, y0)
        x1, y1 = min(out.width, x1), min(out.height, y1)
        if x1 - x0 < 2 or y1 - y0 < 2:
            continue
        region = out.crop((x0, y0, x1, y1))
        region = region.filter(ImageFilter.GaussianBlur(radius=max(6, (x1 - x0) / 14)))
        region = Image.eval(region, lambda v: int(v * 0.72))
        out.paste(region, (x0, y0))
    return out


def rounded_mask(size: tuple[int, int], radius: int) -> Image.Image:
    m = Image.new("L", size, 0)
    ImageDraw.Draw(m).rounded_rectangle([0, 0, size[0] - 1, size[1] - 1], radius, fill=255)
    return m


def compose(shot: Shot, src: Image.Image, canvas_size: tuple[int, int],
            mark: Image.Image | None) -> Image.Image:
    W, H = canvas_size
    margin = int(W * 0.033)
    header_h = int(H * 0.082)
    gap = int(H * 0.024)

    canvas = vertical_gradient(canvas_size, BG_TOP, BG_BOTTOM).convert("RGBA")
    canvas = add_glow(canvas, canvas_size)
    d = ImageDraw.Draw(canvas)

    # ---- 标题栏
    f_title = load_font(int(H * 0.028))
    f_sub = load_font(int(H * 0.0185))
    tx = margin
    if mark is not None:
        ms = int(H * 0.038)
        m2 = mark.resize((ms, ms), Image.LANCZOS)
        canvas.alpha_composite(m2, (margin, margin + (header_h - ms) // 2))
        tx = margin + ms + int(W * 0.011)
    ty = margin + (header_h - int(H * 0.028)) // 2 - int(H * 0.002)
    d.text((tx, ty), shot.title, font=f_title, fill=TITLE_COLOR)
    if shot.sub:
        sx = tx + d.textlength(shot.title, font=f_title) + int(W * 0.016)
        sy = margin + (header_h - int(H * 0.0185)) // 2 + int(H * 0.006)
        d.text((sx, sy), shot.sub, font=f_sub, fill=SUB_COLOR)

    # ---- 界面卡片
    box_w = W - margin * 2
    box_h = H - margin - header_h - gap - margin
    scale = min(box_w / src.width, box_h / src.height)
    tw, th = max(1, int(src.width * scale)), max(1, int(src.height * scale))
    card = src.resize((tw, th), Image.LANCZOS).convert("RGBA")

    px = (W - tw) // 2
    py = margin + header_h + gap + (box_h - th) // 2

    shadow = Image.new("RGBA", canvas_size, (0, 0, 0, 0))
    ImageDraw.Draw(shadow).rounded_rectangle(
        [px - 4, py + 14, px + tw + 4, py + th + 20], 16, fill=(0, 0, 0, 165))
    canvas = Image.alpha_composite(canvas, shadow.filter(ImageFilter.GaussianBlur(22)))
    d = ImageDraw.Draw(canvas)

    canvas.paste(card, (px, py), rounded_mask((tw, th), 12))
    d.rounded_rectangle([px, py, px + tw - 1, py + th - 1], 12,
                        outline=CARD_BORDER, width=1)
    return canvas.convert("RGB")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--size", default="1920x1080",
                    help="画布尺寸。**默认 1920x1080** —— 商店表单硬要求宽高比 16:9、"
                         "单边 1080–7680 像素、每张 ≤15 MB；1920x1200 那种 16:10 会被拒")
    ap.add_argument("--out", default="docs/store", help="输出目录（相对仓库根）")
    ap.add_argument("--verify", action="store_true", help="额外输出打码核对图")
    args = ap.parse_args()

    W, H = (int(v) for v in args.size.lower().split("x"))
    out_dir = os.path.join(REPO, args.out)
    os.makedirs(out_dir, exist_ok=True)

    mark_path = os.path.join(REPO, "public/brand/nexterm-mark-256.png")
    mark = Image.open(mark_path).convert("RGBA") if os.path.exists(mark_path) else None

    rc, skipped = 0, []
    for shot in SHOTS:
        src_path = os.path.join(REPO, shot.src)
        if not os.path.exists(src_path):
            skipped.append(shot.out)
            print(f"[skip] {shot.out}: 缺 {shot.src}", file=sys.stderr)
            continue
        src = Image.open(src_path).convert("RGB")
        boxes = shot.redact or REDACT.get(shot.src, [])
        src = apply_redactions(src, boxes)
        if args.verify and boxes:
            vdir = os.path.join(out_dir, "_verify")
            os.makedirs(vdir, exist_ok=True)
            check = src.copy()
            ImageDraw.Draw(check).rectangle(
                [min(b[0] for b in boxes), min(b[1] for b in boxes),
                 max(b[2] for b in boxes), max(b[3] for b in boxes)],
                outline=(255, 0, 0), width=2)
            check.save(os.path.join(vdir, shot.out))
        out = compose(shot, src, (W, H), mark)
        dst = os.path.join(out_dir, shot.out)
        out.save(dst)
        print(f"[ok]   {shot.out:28s} {out.size[0]}x{out.size[1]}  "
              f"{os.path.getsize(dst) / 1024:7.1f} KB  <- {shot.src}")
        rc += 1

    print(f"\n{rc} 张 -> {os.path.relpath(out_dir, REPO)}/")
    if skipped:
        print(f"跳过 {len(skipped)}: {', '.join(skipped)}（补 raw 后重跑）")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
