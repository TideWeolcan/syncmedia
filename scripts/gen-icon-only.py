#!/usr/bin/env python3
"""
生成 SyncMedia 高清图标（192x192，渐变背景）
"""
import struct, zlib, sys, os, math

def create_png(width, height, pixels):
    """pixels: list of (r,g,b,a) rows"""
    raw = bytearray()
    for y in range(height):
        raw.append(0)  # filter: None
        for x in range(width):
            r, g, b, a = pixels[y][x]
            raw.extend([r, g, b, a])

    def chunk(ctype, data):
        c = ctype + data
        return struct.pack(">I", len(data)) + c + struct.pack(">I", zlib.crc32(c) & 0xFFFFFFFF)

    png = b"\x89PNG\r\n\x1a\n"
    png += chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(bytes(raw), 9))
    png += chunk(b"IEND", b"")
    return png

def lerp(a, b, t):
    return int(a + (b - a) * t)

def lerp_color(c1, c2, t):
    return tuple(lerp(c1[i], c2[i], t) for i in range(4))

def gen_icon(size):
    """生成 SyncMedia 图标：对角渐变 + 圆角方形 + 播放按钮"""
    pixels = []
    cx, cy = size / 2, size / 2

    # 颜色
    bg_top = (30, 41, 59, 255)      # #1e293b 深蓝灰
    bg_bot = (15, 23, 42, 255)      # #0f172a 更深
    icon_bg = (56, 189, 248, 255)   # #38bdf8 天蓝
    icon_bg_dark = (14, 165, 233, 255)  # #0ea5e9
    play_white = (255, 255, 255, 255)  # 白色播放按钮

    # 圆角半径
    corner_r = int(size * 0.22)
    # 内边距
    pad = int(size * 0.12)

    for y in range(size):
        row = []
        for x in range(size):
            # 1. 全局渐变背景（对角线）
            t = (x + y) / (2 * size)
            color = lerp_color(bg_top, bg_bot, t)

            # 2. 圆角矩形卡片
            in_card = pad <= x < size - pad and pad <= y < size - pad
            if in_card:
                dx = dy = 0
                if x < pad + corner_r:
                    dx = pad + corner_r - x
                elif x > size - pad - corner_r:
                    dx = size - pad - corner_r - x
                if y < pad + corner_r:
                    dy = pad + corner_r - y
                elif y > size - pad - corner_r:
                    dy = size - pad - corner_r - y

                if dx > 0 and dy > 0 and dx * dx + dy * dy > corner_r * corner_r:
                    # 圆角外，保持背景
                    pass
                else:
                    # 圆角内，卡片渐变
                    card_t = (x - pad + y - pad) / (2 * (size - 2 * pad))
                    color = lerp_color(icon_bg, icon_bg_dark, card_t)

            # 3. 播放三角形（白色，居中）
            tri_size = int(size * 0.22)
            tri_cx = cx + tri_size * 0.15  # 稍微偏右视觉居中
            tri_cy = cy

            # 三角形三个顶点
            p1 = (tri_cx - tri_size * 0.4, tri_cy - tri_size * 0.55)  # 上
            p2 = (tri_cx - tri_size * 0.4, tri_cy + tri_size * 0.55)  # 下
            p3 = (tri_cx + tri_size * 0.6, tri_cy)                   # 右

            def sign(px, py, ax, ay, bx, by):
                return (px - bx) * (ay - by) - (ax - bx) * (py - by)

            d1 = sign(x, y, p1[0], p1[1], p3[0], p3[1])
            d2 = sign(x, y, p3[0], p3[1], p2[0], p2[1])
            d3 = sign(x, y, p2[0], p2[1], p1[0], p1[1])

            has_neg = d1 < 0 or d2 < 0 or d3 < 0
            has_pos = d1 > 0 or d2 > 0 or d3 > 0

            if in_card and not (has_neg and has_pos):
                color = play_white

            row.append(color)
        pixels.append(row)

    return pixels

def main():
    out = sys.argv[1] if len(sys.argv) > 1 else "ic_launcher.png"
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    pixels = gen_icon(192)
    with open(out, "wb") as f:
        f.write(create_png(192, 192, pixels))
    print(f"图标: {out} ({os.path.getsize(out)} bytes)")

if __name__ == "__main__":
    main()
