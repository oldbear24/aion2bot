"""Draws the bot's interface icons in the style of its role and class icons.

Each icon is a 128x128 PNG: a rounded square (or a circle, like the class
icons) with a darker rim and a soft top-to-bottom gradient, carrying a bold
white glyph whose shaded side is white tinted 20% with the base colour.
They are drawn at 4x and scaled down.

    python tools/icons/draw_ui_icons.py                  # writes internal/emoji/images/ui
    python tools/icons/draw_ui_icons.py --sheet out.png  # and a preview next to the others

The file names are the ids internal/bot/icons.go maps the bot's emojis to.
Pillow is the only requirement.
"""

import colorsys
import math
import os
import sys

from PIL import Image, ImageDraw

S = 4
N = 128 * S

GREEN = "32c167"
BLUE = "4887ec"
RED = "e75050"
GOLD = "f0b12c"
ORANGE = "ee8a35"
SLATE = "7a8798"
PURPLE = "8b6cdf"
TEAL = "26ad9f"
NAVY = "4f6fae"
BROWN = "b8894a"
SKY = "3a9fdc"


def rgb(h):
    return tuple(int(h[i:i + 2], 16) for i in (0, 2, 4))


def lighten(c, dl):
    h, l, s = colorsys.rgb_to_hls(*(v / 255 for v in c))
    return tuple(round(v * 255) for v in colorsys.hls_to_rgb(h, max(0, min(1, l + dl)), s))


def mix(a, b, t):
    return tuple(round(x + (y - x) * t) for x, y in zip(a, b))


def sc(v):
    return v * S


def rot(pts, deg, c=(64, 64)):
    a = math.radians(deg)
    ca, sa = math.cos(a), math.sin(a)
    return [(c[0] + (x - c[0]) * ca - (y - c[1]) * sa, c[1] + (x - c[0]) * sa + (y - c[1]) * ca) for x, y in pts]


class Glyph:
    """White shapes drawn in 128-space. cut=True erases; tint=True adds a
    shape in the shaded tone; shade() and right() only recolour."""

    def __init__(self):
        self.m = Image.new("L", (N, N), 0)
        self.t = Image.new("L", (N, N), 0)
        self.dm = ImageDraw.Draw(self.m)
        self.dt = ImageDraw.Draw(self.t)

    def _do(self, fn, cut, tint):
        if cut:
            fn(self.dm, 0)
        else:
            fn(self.dm, 255)
            fn(self.dt, 255 if tint else 0)

    def shade(self, pts):
        self.dt.polygon([(sc(x), sc(y)) for x, y in pts], fill=255)

    def right(self, x=64):
        self.shade([(x, 0), (128, 0), (128, 128), (x, 128)])

    def poly(self, pts, cut=False, tint=False):
        self._do(lambda d, v: d.polygon([(sc(x), sc(y)) for x, y in pts], fill=v), cut, tint)

    def circle(self, c, r, cut=False, tint=False):
        x, y = c
        self._do(lambda d, v: d.ellipse([sc(x - r), sc(y - r), sc(x + r), sc(y + r)], fill=v), cut, tint)

    def ellipse(self, box, cut=False, tint=False):
        self._do(lambda d, v: d.ellipse([sc(b) for b in box], fill=v), cut, tint)

    def rrect(self, box, r, cut=False, tint=False):
        self._do(lambda d, v: d.rounded_rectangle([sc(b) for b in box], radius=sc(r), fill=v), cut, tint)

    def capsule(self, p1, p2, w, cut=False, tint=False):
        (x1, y1), (x2, y2) = p1, p2
        n = math.hypot(x2 - x1, y2 - y1) or 1
        ox, oy = -(y2 - y1) / n * w / 2, (x2 - x1) / n * w / 2
        self.poly([(x1 + ox, y1 + oy), (x2 + ox, y2 + oy), (x2 - ox, y2 - oy), (x1 - ox, y1 - oy)], cut, tint)
        self.circle(p1, w / 2, cut, tint)
        self.circle(p2, w / 2, cut, tint)

    def line(self, pts, w, cut=False, tint=False):
        for a, b in zip(pts, pts[1:]):
            self.capsule(a, b, w, cut, tint)

    def arc(self, c, r, a0, a1, w, cut=False, tint=False):
        steps = max(2, int(abs(a1 - a0) / 4))
        pts = [(c[0] + r * math.cos(math.radians(a0 + (a1 - a0) * i / steps)),
                c[1] + r * math.sin(math.radians(a0 + (a1 - a0) * i / steps))) for i in range(steps + 1)]
        self.line(pts, w, cut, tint)

    def ring(self, c, r, w, cut=False, tint=False):
        self.arc(c, r, 0, 360, w, cut, tint)


def render(base_hex, draw, shape="square"):
    base = rgb(base_hex)
    img = Image.new("RGBA", (N, N), (0, 0, 0, 0))

    outer = Image.new("L", (N, N), 0)
    inner = Image.new("L", (N, N), 0)
    do, di = ImageDraw.Draw(outer), ImageDraw.Draw(inner)
    if shape == "circle":
        do.ellipse([sc(1.5), sc(1.5), sc(126.5), sc(126.5)], fill=255)
        di.ellipse([sc(6), sc(6), sc(122), sc(122)], fill=255)
    else:
        do.rounded_rectangle([sc(1.5), sc(1.5), sc(126.5), sc(126.5)], radius=sc(26), fill=255)
        di.rounded_rectangle([sc(6), sc(6), sc(122), sc(122)], radius=sc(21.5), fill=255)

    img.paste(lighten(base, -0.19) + (255,), (0, 0), outer)
    top, bottom = lighten(base, 0.055), lighten(base, -0.045)
    grad = Image.new("RGBA", (1, N))
    for y in range(N):
        grad.putpixel((0, y), mix(top, bottom, y / (N - 1)) + (255,))
    img.paste(grad.resize((N, N)), (0, 0), inner)

    g = Glyph()
    draw(g)
    tint = Image.new("RGBA", (N, N), mix((255, 255, 255), base, 0.2) + (255,))
    white = Image.new("RGBA", (N, N), (255, 255, 255, 255))
    glyph = Image.composite(tint, white, g.t)
    img = Image.composite(glyph, img, g.m)
    img.putalpha(Image.composite(img.getchannel("A"), Image.new("L", (N, N), 0), outer))
    return img.convert("RGBa").resize((128, 128), Image.LANCZOS).convert("RGBA")


# ---- glyphs, in 128-space ---------------------------------------------------

def join(g):  # raised hand
    g.rrect((42, 58, 86, 98), 16)
    for x, top in ((47, 36), (58, 28), (69, 30), (80, 38)):
        g.capsule((x, top), (x, 70), 10.5)
    g.capsule((44, 84), (31, 63), 11)
    for x in (52.5, 63.5, 74.5):
        g.capsule((x, 50), (x, 64), 2.2, cut=True)
    g.right()


def reserve(g):  # chair
    g.rrect((42, 26, 86, 60), 11)
    g.rrect((51, 34, 77, 52), 5, cut=True)
    g.rrect((34, 60, 94, 74), 6, tint=True)
    g.capsule((44, 74), (44, 98), 10)
    g.capsule((84, 74), (84, 98), 10)


def manage(g):  # gear
    c, r = (64, 64), 25
    g.circle(c, r)
    for i in range(8):
        g.poly(rot([(57.5, c[1] - r - 9), (70.5, c[1] - r - 9), (72.5, c[1] - r + 3), (55.5, c[1] - r + 3)], i * 45, c))
    g.circle(c, 10, cut=True)
    g.right()


def clock(g, c=(64, 64), r=31, h1=(64, 44), h2=(79, 64)):
    g.circle(c, r)
    g.capsule(c, h1, 7, cut=True)
    g.capsule(c, h2, 7, cut=True)
    g.circle(c, 5.5, cut=True)
    g.right(c[0])


def start(g):
    clock(g)


def ok(g):
    g.line([(38, 66), (56, 84), (90, 46)], 15)


def hourglass(g, sand_top):
    g.rrect((38, 28, 90, 38), 5)
    g.rrect((38, 90, 90, 100), 5)
    g.poly([(44, 36), (84, 36), (84, 42), (66, 64), (84, 86), (84, 92), (44, 92), (44, 86), (62, 64), (44, 42)])
    g.poly([(52, 44), (76, 44), (64, 58)], cut=True)
    g.poly([(64, 70), (78, 86), (50, 86)], cut=True)
    if sand_top:
        g.poly([(57, 50), (71, 50), (64, 58)], tint=True)
        g.poly([(64, 76), (74, 86), (54, 86)], tint=True)
    else:
        g.poly([(64, 68), (79, 86), (49, 86)], tint=True)


def waiting(g):
    hourglass(g, sand_top=True)


def expired(g):
    hourglass(g, sand_top=False)


def lock_body(g):
    g.rrect((38, 56, 90, 98), 10)
    g.shade([(64, 56), (92, 56), (92, 100), (64, 100)])
    g.circle((64, 72), 6, cut=True)
    g.capsule((64, 74), (64, 86), 5, cut=True)


def closed(g):
    g.arc((64, 50), 16, 180, 360, 9)
    g.capsule((48, 50), (48, 60), 9)
    g.capsule((80, 50), (80, 60), 9)
    lock_body(g)


def reopen(g):
    g.arc((80, 40), 16, 180, 360, 9)
    g.capsule((64, 40), (64, 60), 9)
    g.capsule((96, 40), (96, 46), 9)
    lock_body(g)


def edit(g):  # pencil, tip at the bottom left
    g.poly(rot([(40, 56), (86, 56), (86, 72), (40, 72)], -45))
    g.poly(rot([(86, 56), (96, 56), (96, 72), (86, 72)], -45), tint=True)
    g.circle(rot([(96, 64)], -45)[0], 8, tint=True)
    g.poly(rot([(40, 56), (40, 72), (24, 64)], -45))
    g.poly(rot([(30, 61), (30, 67), (24, 64)], -45), cut=True)
    g.capsule(*rot([(46, 64), (82, 64)], -45), 2.5, cut=True)


def map_(g):  # folded map
    g.poly([(32, 40), (52, 32), (52, 88), (32, 96)])
    g.poly([(52, 32), (76, 40), (76, 96), (52, 88)], tint=True)
    g.poly([(76, 40), (96, 32), (96, 88), (76, 96)])
    g.circle((64, 56), 6, cut=True)


def delete(g):  # bin
    g.rrect((36, 34, 92, 44), 5)
    g.rrect((54, 27, 74, 38), 5)
    g.rrect((59, 31, 69, 36), 2, cut=True)
    g.poly([(42, 48), (86, 48), (81, 96), (47, 96)])
    g.shade([(64, 48), (86, 48), (81, 96), (64, 96)])
    for x in (55, 64, 73):
        g.capsule((x, 58), (x, 86), 5, cut=True)


def post(g):  # megaphone
    g.poly([(42, 54), (80, 36), (80, 92), (42, 74)])
    g.rrect((32, 52, 46, 76), 5)
    g.poly([(48, 72), (60, 72), (62, 94), (52, 94)], tint=True)
    g.rrect((78, 32, 88, 96), 5)
    g.arc((88, 64), 12, -45, 45, 5)


def accepted(g):  # party popper
    g.poly([(30, 98), (48, 50), (78, 80)])
    g.shade([(30, 98), (63, 65), (78, 80)])
    g.capsule((48, 50), (78, 80), 7)
    for (x, y), r in (((66, 36), 5), ((92, 60), 5), ((84, 30), 4), ((96, 42), 3.5), ((54, 30), 3.5)):
        g.circle((x, y), r)
    g.arc((76, 52), 10, 200, 290, 5)
    g.capsule((78, 46), (88, 38), 5, tint=True)


def reminder(g):  # alarm clock
    g.circle((44, 38), 10)
    g.circle((84, 38), 10)
    g.capsule((46, 90), (40, 98), 7)
    g.capsule((82, 90), (88, 98), 7)
    g.circle((64, 68), 31, cut=True)
    g.circle((64, 68), 28)
    g.right()
    g.capsule((64, 68), (64, 51), 6.5, cut=True)
    g.capsule((64, 68), (76, 76), 6.5, cut=True)
    g.circle((64, 68), 5, cut=True)


def ingame(g):  # gamepad
    g.rrect((28, 46, 100, 84), 19)
    g.circle((42, 84), 13)
    g.circle((86, 84), 13)
    g.right()
    g.capsule((36, 64), (52, 64), 6, cut=True)
    g.capsule((44, 56), (44, 72), 6, cut=True)
    g.circle((80, 59), 4.5, cut=True)
    g.circle((90, 68), 4.5, cut=True)


def playing(g):  # target
    g.circle((64, 64), 33)
    g.circle((64, 64), 25, cut=True)
    g.circle((64, 64), 18)
    g.circle((64, 64), 11, cut=True)
    g.circle((64, 64), 6, tint=True)


def gs(g):  # rising chart
    g.line([(36, 32), (36, 94), (94, 94)], 8)
    g.line([(46, 80), (60, 64), (72, 72), (88, 50)], 8)
    g.poly([(80, 42), (96, 40), (93, 56)])


def note(g):
    g.rrect((40, 28, 88, 100), 9)
    g.right()
    for y in (46, 60, 74):
        g.capsule((50, y), (78, y), 5, cut=True)
    g.capsule((50, 88), (66, 88), 5, cut=True)


def any_(g):  # die
    g.rrect((34, 34, 94, 94), 14)
    g.right()
    for c in ((48, 48), (80, 48), (64, 64), (48, 80), (80, 80)):
        g.circle(c, 6, cut=True)


def error(g):
    g.capsule((42, 42), (86, 86), 15)
    g.capsule((86, 42), (42, 86), 15)


def warning(g):
    pts = [(64, 30), (98, 92), (30, 92)]
    g.line(pts + [pts[0]], 14)
    g.poly(pts)
    g.capsule((64, 50), (64, 70), 8, cut=True)
    g.circle((64, 82), 5, cut=True)


def crystal(g):  # transcendence
    g.poly([(64, 26), (96, 60), (64, 102), (32, 60)])
    g.shade([(64, 26), (96, 60), (64, 102)])
    g.poly([(64, 40), (82, 60), (64, 84), (46, 60)], cut=True)
    g.poly([(64, 46), (77, 60), (64, 78), (51, 60)])


def anchor(g):  # subjugation
    g.ring((64, 34), 7, 6)
    g.capsule((64, 42), (64, 96), 9)
    g.capsule((48, 52), (80, 52), 8)
    g.arc((64, 70), 27, 15, 165, 8)
    g.poly([(98, 72), (84, 70), (92, 84)])
    g.poly([(30, 72), (44, 70), (36, 84)])


def temple(g):  # raid
    g.poly([(30, 46), (64, 26), (98, 46)])
    g.rrect((32, 46, 96, 54), 2)
    for x in (40, 55, 73, 88):
        g.rrect((x - 4.5, 58, x + 4.5, 86), 2, tint=x > 64)
    g.rrect((30, 88, 98, 98), 3)
    g.circle((64, 39), 4, cut=True)


def bell(g):
    g.circle((64, 30), 5)
    g.poly([(44, 76), (46, 50), (54, 38), (64, 34), (74, 38), (82, 50), (84, 76), (94, 86), (34, 86)])
    g.circle((64, 52), 20)
    g.rrect((32, 82, 96, 90), 4)
    g.circle((64, 96), 7)
    g.right()


def alert(g):
    bell(g)


def alert_off(g):
    bell(g)
    g.capsule((34, 30), (94, 98), 14, cut=True)
    g.capsule((34, 30), (94, 98), 7)


def find(g):  # magnifier
    g.ring((56, 56), 20, 10)
    g.circle((56, 56), 15, tint=True)
    g.capsule((72, 72), (92, 92), 13)


def stats(g):
    for x0, y0 in ((34, 64), (57, 44), (80, 28)):
        g.rrect((x0, y0, x0 + 16, 96), 5, tint=x0 == 57)


def link(g):
    for p1, p2 in (((40, 88), (60, 68)), ((68, 60), (88, 40))):
        g.capsule(p1, p2, 26)
        g.capsule(p1, p2, 10, cut=True)
    g.capsule((54, 74), (74, 54), 9)


def pin(g):  # map pin
    g.circle((64, 52), 25)
    g.poly([(42, 64), (86, 64), (64, 102)])
    g.right()
    g.circle((64, 52), 10, cut=True)


def language(g):  # globe
    g.circle((64, 64), 33)
    g.right()
    g.ellipse((50, 31, 78, 97), cut=True)
    g.ellipse((55, 33, 73, 95))
    g.ellipse((60, 31, 68, 97), cut=True)
    g.capsule((31, 64), (97, 64), 5, cut=True)
    g.capsule((38, 47), (90, 47), 4.5, cut=True)
    g.capsule((38, 81), (90, 81), 4.5, cut=True)


def players(g):
    g.circle((80, 44), 13, tint=True)
    g.rrect((60, 62, 100, 96), 18, tint=True)
    g.circle((52, 48), 15, cut=True)
    g.rrect((26, 66, 82, 102), 22, cut=True)
    g.circle((52, 48), 13)
    g.rrect((30, 70, 76, 98), 20)
    g.rrect((30, 92, 100, 104), 0, cut=True)


def top(g):  # trophy
    g.ring((40, 46), 10, 6)
    g.ring((88, 46), 10, 6)
    g.poly([(40, 30), (88, 30), (86, 52), (76, 66), (52, 66), (42, 52)])
    g.circle((64, 46), 23)
    g.rrect((58, 64, 70, 82), 2)
    g.rrect((44, 82, 84, 96), 5)
    g.right()
    g.rrect((30, 18, 98, 30), 0, cut=True)


def server(g):  # home
    g.poly([(64, 28), (100, 60), (28, 60)])
    g.line([(64, 28), (100, 60)], 8)
    g.line([(64, 28), (28, 60)], 8)
    g.rrect((38, 58, 90, 98), 5)
    g.right()
    g.rrect((56, 72, 72, 98), 3, cut=True)


def timer(g):  # stopwatch
    g.rrect((56, 24, 72, 33), 3)
    g.capsule((64, 30), (64, 40), 7)
    g.capsule((88, 38), (93, 43), 7)
    clock(g, c=(64, 70), r=28, h1=(64, 52), h2=(64, 70))
    g.capsule((64, 70), (76, 58), 6.5, cut=True)


def open_(g):
    g.circle((64, 64), 26)
    g.circle((64, 64), 16, cut=True)
    g.circle((64, 64), 10, tint=True)


def help_(g):  # open book
    g.poly([(30, 38), (60, 34), (64, 40), (64, 96), (60, 90), (30, 94)])
    g.poly([(98, 38), (68, 34), (64, 40), (64, 96), (68, 90), (98, 94)])
    g.right()
    for y in (50, 62, 74):
        g.capsule((38, y), (54, y - 2), 4, cut=True)
        g.capsule((74, y - 2), (90, y), 4, cut=True)


def admin(g):  # wrench
    g.capsule((40, 88), (70, 58), 13)
    g.circle((76, 52), 17)
    g.poly(rot([(70, 30), (82, 30), (82, 52), (70, 52)], 45, (76, 52)), cut=True)
    g.circle((40, 88), 3.5, cut=True)


def legend(g):  # the shapes on the icons
    g.rrect((32, 32, 60, 60), 7)
    g.circle((82, 46), 15, tint=True)
    g.poly([(46, 68), (62, 96), (30, 96)], tint=True)
    g.line([(46, 68), (62, 96), (30, 96), (46, 68)], 6, tint=True)
    g.capsule((74, 82), (96, 82), 9)


def unknown(g):  # question mark
    g.arc((64, 50), 15, 180, 395, 11)
    g.capsule((76, 60), (64, 70), 11)
    g.capsule((64, 70), (64, 76), 11)
    g.circle((64, 93), 6.5)


ICONS = {
    "join": (GREEN, join), "reserve": (BLUE, reserve), "manage": (SLATE, manage), "start": (TEAL, start),
    "ok": (GREEN, ok), "waiting": (GOLD, waiting), "expired": (SLATE, expired), "closed": (SLATE, closed),
    "reopen": (GREEN, reopen), "edit": (PURPLE, edit), "map": (TEAL, map_), "delete": (RED, delete),
    "post": (ORANGE, post), "accepted": (GOLD, accepted), "reminder": (ORANGE, reminder), "ingame": (PURPLE, ingame),
    "playing": (RED, playing), "gs": (BLUE, gs), "note": (NAVY, note), "any": (PURPLE, any_), "error": (RED, error),
    "warning": (GOLD, warning), "crystal": (SKY, crystal), "anchor": (NAVY, anchor), "temple": (BROWN, temple),
    "alert": (GOLD, alert), "alert_off": (SLATE, alert_off), "find": (BLUE, find), "stats": (TEAL, stats),
    "link": (BLUE, link), "pin": (RED, pin), "language": (SKY, language), "players": (BLUE, players),
    "top": (GOLD, top), "server": (NAVY, server), "timer": (TEAL, timer), "open": (GREEN, open_),
    "help": (NAVY, help_), "admin": (SLATE, admin), "legend": (PURPLE, legend),
    "unknown": (SLATE, unknown, "circle"),
}


def sheet(path, root, icons):
    existing = os.path.join(root, "internal", "emoji", "images")
    refs = [Image.open(os.path.join(existing, f)).convert("RGBA")
            for f in sorted(os.listdir(existing)) if f.endswith(".png")]
    tiles = refs + list(icons.values())
    cols = 10
    rows = math.ceil(len(tiles) / cols)
    big = Image.new("RGBA", (cols * 140 + 10, rows * 140 + 10), (49, 51, 56, 255))
    small = Image.new("RGBA", (cols * 30 + 6, rows * 30 + 6), (49, 51, 56, 255))
    for i, t in enumerate(tiles):
        big.alpha_composite(t, (10 + (i % cols) * 140, 10 + (i // cols) * 140))
        small.alpha_composite(t.resize((24, 24), Image.LANCZOS), (6 + (i % cols) * 30, 6 + (i // cols) * 30))
    big.save(path)
    small.save(path.replace(".png", "_small.png"))


def main():
    root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    out = os.path.join(root, "internal", "emoji", "images", "ui")
    os.makedirs(out, exist_ok=True)
    icons = {}
    for name, (color, fn, *shape) in ICONS.items():
        icons[name] = render(color, fn, *shape)
        icons[name].save(os.path.join(out, name + ".png"), optimize=True)
    if "--sheet" in sys.argv:
        sheet(sys.argv[sys.argv.index("--sheet") + 1], root, icons)


if __name__ == "__main__":
    main()
