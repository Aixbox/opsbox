# 生成 opsbox 应用图标：与 build/appicon.svg 同构，输出 build/appicon.png（1024x1024，wails build 用它生成 exe 图标）。
# 用法：python scripts/gen_appicon.py
from pathlib import Path

from PIL import Image, ImageDraw

CANVAS = 680          # 设计稿尺寸（与 svg viewBox 一致）
RENDER = 2048         # 超采样渲染，缩到 1024 得到平滑边缘
OUT = Path(__file__).resolve().parent.parent / "build" / "appicon.png"
S = RENDER / CANVAS

TOP = (11, 27, 43)     # #0B1B2B
BOTTOM = (21, 51, 78)  # #15334E
CARD = (21, 42, 66)    # #152A42
CARD_STROKE = (66, 84, 107)   # #94A3B8 35% 叠在 CARD 上
SEPARATOR = (53, 72, 96)      # #94A3B8 25% 叠在 CARD 上
CYAN = (34, 211, 238)  # #22D3EE
CURSOR = (226, 232, 240)      # #E2E8F0
DOTS = [(248, 113, 113), (251, 191, 36), (52, 211, 153)]  # 红 / 黄 / 绿

img = Image.new("RGB", (RENDER, RENDER))
draw = ImageDraw.Draw(img)

# 垂直渐变底：先造 1px 高的渐变条再拉伸
gradient = Image.new("RGB", (1, RENDER))
for y in range(RENDER):
    t = y / (RENDER - 1)
    gradient.putpixel((0, y), tuple(round(a + (b - a) * t) for a, b in zip(TOP, BOTTOM)))
img.paste(gradient.resize((RENDER, RENDER)), (0, 0))

# 圆角底板（24,24,656x656,r144）
draw.rounded_rectangle(
    [round(24 * S), round(24 * S), round(656 * S), round(656 * S)],
    radius=round(144 * S), fill=TOP,
)
# 用渐变重铺底板区域：底板 = 渐变图 + 圆角蒙版
mask = Image.new("L", (RENDER, RENDER), 0)
ImageDraw.Draw(mask).rounded_rectangle(
    [round(24 * S), round(24 * S), round(656 * S), round(656 * S)],
    radius=round(144 * S), fill=255,
)
img.paste(gradient.resize((RENDER, RENDER)), (0, 0), mask)

# 终端窗口卡片
draw.rounded_rectangle(
    [round(132 * S), round(176 * S), round(548 * S), round(504 * S)],
    radius=round(36 * S), fill=CARD, outline=CARD_STROKE, width=round(3 * S),
)

# 标题栏三色圆点
for i, color in enumerate(DOTS):
    cx, cy, r = 178 + i * 32, 222, 13
    box = [round((cx - r) * S), round((cy - r) * S), round((cx + r) * S), round((cy + r) * S)]
    draw.ellipse(box, fill=color)

# 标题栏分隔线
draw.line(
    [round(132 * S), round(252 * S), round(548 * S), round(252 * S)],
    fill=SEPARATOR, width=round(3 * S),
)

# 提示符 >：两段折线 + 端点圆模拟圆头
chevron = [(196, 318), (258, 378), (196, 438)]
width = round(34 * S)
pts = [(round(x * S), round(y * S)) for x, y in chevron]
draw.line(pts, fill=CYAN, width=width, joint="curve")
r_cap = width / 2
for px, py in (pts[0], pts[-1]):
    draw.ellipse([px - r_cap, py - r_cap, px + r_cap, py + r_cap], fill=CYAN)

# 光标 _
draw.rounded_rectangle(
    [round(296 * S), round(410 * S), round(408 * S), round(442 * S)],
    radius=round(16 * S), fill=CURSOR,
)

# 透明化底板外区域（PNG 四角透明，适合圆角展示）
rgba = img.convert("RGBA")
alpha = Image.new("L", (RENDER, RENDER), 0)
ImageDraw.Draw(alpha).rounded_rectangle(
    [round(24 * S), round(24 * S), round(656 * S), round(656 * S)],
    radius=round(144 * S), fill=255,
)
rgba.putalpha(alpha)

OUT.parent.mkdir(parents=True, exist_ok=True)
rgba.resize((1024, 1024), Image.LANCZOS).save(OUT)
print(f"written {OUT}")
