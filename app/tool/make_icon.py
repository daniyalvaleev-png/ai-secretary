"""Рисует иконку ИИ-секретаря: три «звёздочки» (как на экране входа) на фиолетовом градиенте.

Выход (в папку assets/icon рядом с pubspec.yaml):
  icon.png        — 1024×1024, скруглённый квадрат: для веба и старых Android
  background.png  — 1024×1024, только градиент: фон адаптивной иконки Android
  foreground.png  — 1024×1024, только звёздочки на прозрачном: передний план адаптивной иконки
Запуск (из папки app): python tool/make_icon.py .
"""
import math
import sys
from pathlib import Path

from PIL import Image, ImageDraw

S = 1024
SS = 4  # рисуем в 4 раза крупнее и уменьшаем — так края гладкие
TOP_LEFT = (0x4F, 0x46, 0xE5)  # индиго, как в теме приложения
BOTTOM_RIGHT = (0x93, 0x33, 0xEA)  # фиолетовый


def gradient(size):
    """Диагональный градиент из левого верхнего угла в правый нижний"""
    small = Image.new("RGB", (256, 256))
    px = small.load()
    for y in range(256):
        for x in range(256):
            t = (x + y) / 510
            px[x, y] = tuple(round(a + (b - a) * t) for a, b in zip(TOP_LEFT, BOTTOM_RIGHT))
    return small.resize((size, size), Image.BICUBIC)


def sparkle(draw, cx, cy, r, fill):
    """Четырёхлучевая звезда с вогнутыми сторонами (астроида): x = r·cos³t, y = r·sin³t"""
    pts = []
    for i in range(720):
        t = 2 * math.pi * i / 720
        c, s = math.cos(t), math.sin(t)
        pts.append((cx + r * c * abs(c) ** 2, cy + r * s * abs(s) ** 2))
    draw.polygon(pts, fill=fill)


def sparkles(scale, offset=(0, 0)):
    """Слой со звёздочками на прозрачном фоне. scale — доля от размера иконки, которую они занимают"""
    big = S * SS
    layer = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    white = (255, 255, 255, 255)
    k = big * scale
    cx, cy = big / 2 + offset[0] * big, big / 2 + offset[1] * big
    # Большая звезда слева снизу и две маленькие справа сверху — как значок auto_awesome
    sparkle(d, cx - 0.12 * k, cy + 0.10 * k, 0.42 * k, white)
    sparkle(d, cx + 0.30 * k, cy - 0.28 * k, 0.17 * k, white)
    sparkle(d, cx + 0.32 * k, cy + 0.30 * k, 0.11 * k, white)
    return layer.resize((S, S), Image.LANCZOS)


def rounded_mask(radius_frac):
    big = S * SS
    m = Image.new("L", (big, big), 0)
    ImageDraw.Draw(m).rounded_rectangle((0, 0, big - 1, big - 1), radius=big * radius_frac, fill=255)
    return m.resize((S, S), Image.LANCZOS)


def main():
    out = Path(sys.argv[1] if len(sys.argv) > 1 else ".") / "assets" / "icon"
    out.mkdir(parents=True, exist_ok=True)

    bg = gradient(S)
    bg.save(out / "background.png")

    # Адаптивная иконка: flutter_launcher_icons ещё уменьшает передний план (отступ 16% с каждой
    # стороны), а Android обрезает края кругом — поэтому здесь звёздочки крупнее, чем в icon.png
    sparkles(0.68).save(out / "foreground.png")

    # Обычная иконка: градиентный скруглённый квадрат со звёздочками
    icon = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    icon.paste(bg, (0, 0), rounded_mask(0.22))
    icon.alpha_composite(sparkles(0.62))
    icon.save(out / "icon.png")
    # Значок уведомлений Android: только белый силуэт на прозрачном (система сама красит его),
    # по размеру на каждую плотность экрана
    res = out.parent.parent / "android" / "app" / "src" / "main" / "res"
    if res.exists():
        stars = sparkles(0.86)
        for folder, px in [("mdpi", 24), ("hdpi", 36), ("xhdpi", 48), ("xxhdpi", 72), ("xxxhdpi", 96)]:
            d = res / f"drawable-{folder}"
            d.mkdir(exist_ok=True)
            stars.resize((px, px), Image.LANCZOS).save(d / "ic_notification.png")
        print("значок уведомлений: res/drawable-*/ic_notification.png")

    print("готово:", ", ".join(p.name for p in sorted(out.iterdir())))


if __name__ == "__main__":
    main()
