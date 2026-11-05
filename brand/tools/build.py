"""Builds the ndb brand SVG files from the mark geometry.

    python3 brand/tools/build.py && brand/tools/render.sh

The mark is a pair of braces with three NDJSON lines inside. The lines have
different lengths, and the last line, the committed line, is the accent color.
The wordmark `ndb` is drawn from geometry, so it needs no font.
"""

import json
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
SVG = ROOT / 'svg'
TEXTS = json.loads((ROOT / 'tools' / 'wordmark.json').read_text())

INK = '#0B0B0F'
PAPER = '#FFFFFF'
ACCENT = '#F0047F'
MUTED = '#6B6B76'
LINE = '#E5E5E5'

DARK_INK = '#F4F4F6'
DARK_PAPER = '#0E0E11'
DARK_MUTED = '#9A9AA5'
DARK_LINE = '#26262C'

ON_LIGHT = {'ink': INK, 'commit': ACCENT}
ON_DARK = {'ink': DARK_INK, 'commit': ACCENT}

PRIMARY = {'top': 7, 'bottom': 57, 'spine': 14, 'radius': 8, 'tip': 4, 'brace': 5.5, 'bar': 5.5, 'ys': (22, 32, 42), 'x': 21, 'lengths': (20, 12, 23)}
SMALL = {'top': 8, 'bottom': 56, 'spine': 14, 'radius': 6, 'tip': 3.5, 'brace': 7, 'bar': 7, 'ys': (22, 32, 42), 'x': 22, 'lengths': (17, 10, 21)}

LETTER_GAPS = (12, 18)
X_HEIGHT = 100
ASCENDER = 145
STEM = 19


def layout(spec):
    """Returns the spec with its ink bounds in the 64 grid. The bounds are
    centered on x. The braces are centered on y by the spec values."""
    left = spec['spine'] - spec['tip'] - spec['brace'] / 2
    top = spec['top'] - spec['brace'] / 2
    bottom = spec['bottom'] + spec['brace'] / 2
    width = 64 - 2 * left

    return {**spec, 'box': (left, top, width, bottom - top)}


def brace(spec, side):
    """One brace as a stroked path. The right brace mirrors the left one around
    the vertical axis of the grid."""
    s, r, t, top, bottom = spec['spine'], spec['radius'], spec['tip'], spec['top'], spec['bottom']
    mid = (top + bottom) / 2
    flip = side == 'right'
    x = lambda value: 64 - value if flip else value
    inward, outward = (0, 1) if flip else (1, 0)

    return (
        f'M{x(s + r):g} {top:g}'
        f'A{r:g} {r:g} 0 0 {outward} {x(s):g} {top + r:g}'
        f'V{mid - t:g}'
        f'A{t:g} {t:g} 0 0 {inward} {x(s - t):g} {mid:g}'
        f'A{t:g} {t:g} 0 0 {inward} {x(s):g} {mid + t:g}'
        f'V{bottom - r:g}'
        f'A{r:g} {r:g} 0 0 {outward} {x(s + r):g} {bottom:g}'
    )


def mark_body(spec, colors=None, classes=False):
    """SVG elements for the mark. With classes, colors come from a style."""
    out = []

    for side in ('left', 'right'):
        paint = 'class="ink"' if classes else f'stroke="{colors["ink"]}"'
        out.append(f'<path d="{brace(spec, side)}" fill="none" {paint} stroke-width="{spec["brace"]}" stroke-linecap="round" stroke-linejoin="round"/>')

    last = len(spec['ys']) - 1

    for index, (y, length) in enumerate(zip(spec['ys'], spec['lengths'])):
        role = 'commit' if index == last else 'ink'
        paint = f'class="{role}"' if classes else f'stroke="{colors[role]}"'
        out.append(f'<path d="M{spec["x"]:g} {y:g}h{length:g}" fill="none" {paint} stroke-width="{spec["bar"]}" stroke-linecap="round"/>')

    return ''.join(out)


def svg(view_box, body, title='ndb'):
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{view_box}" role="img" aria-label="{title}"><title>{title}</title>{body}</svg>\n'


def ndb_letters():
    """The three letters as one path, on a baseline at y = 0 and an x-height of
    100 units. Bowls and arch are half or full rings with a stem width of 19."""
    radius, inner = X_HEIGHT / 2, X_HEIGHT / 2 - STEM
    gap_nd, gap_db = LETTER_GAPS
    ring = lambda cx: (
        f'M{cx - radius:g} {-radius:g}A{radius:g} {radius:g} 0 1 1 {cx + radius:g} {-radius:g}A{radius:g} {radius:g} 0 1 1 {cx - radius:g} {-radius:g}Z'
        f'M{cx - inner:g} {-radius:g}A{inner:g} {inner:g} 0 1 0 {cx + inner:g} {-radius:g}A{inner:g} {inner:g} 0 1 0 {cx - inner:g} {-radius:g}Z'
    )
    stem = lambda x, top: f'M{x:g} {-top:g}h{STEM}V0h{-STEM}Z'

    n_x = 0
    d_x = n_x + X_HEIGHT + gap_nd
    b_x = d_x + X_HEIGHT + gap_db

    arch = (
        f'M{n_x:g} {-radius:g}A{radius:g} {radius:g} 0 0 1 {n_x + X_HEIGHT:g} {-radius:g}'
        f'V0h{-STEM}V{-radius:g}A{inner:g} {inner:g} 0 0 0 {n_x + STEM:g} {-radius:g}Z'
    )

    path = (
        stem(n_x, X_HEIGHT) + arch
        + ring(d_x + radius) + stem(d_x + X_HEIGHT - STEM, ASCENDER)
        + stem(b_x, ASCENDER) + ring(b_x + radius)
    )

    return path, [0, -ASCENDER, b_x + X_HEIGHT, 0]


NDB_PATH, NDB_BOUNDS = ndb_letters()
TEXTS['wordmark'] = {'path': NDB_PATH, 'bounds': NDB_BOUNDS, 'x_height': X_HEIGHT}


def box_string(box):
    return ' '.join(f'{value:.3f}' for value in box)


def text_group(name, x, baseline, x_height, color):
    """Places an outlined text so that its x-height is x_height units."""
    entry = TEXTS[name]
    scale = x_height / entry['x_height']

    return f'<path transform="translate({x:.3f} {baseline:.3f}) scale({scale:.6f})" d="{entry["path"]}" fill="{color}"/>', scale


def ink(name, scale):
    left, top, right, bottom = TEXTS[name]['bounds']

    return left * scale, top * scale, right * scale, bottom * scale


def placed_mark(spec, colors, x, y, height):
    bx, by, bw, bh = spec['box']
    scale = height / bh

    return f'<g transform="translate({x:.3f} {y:.3f}) scale({scale:.6f}) translate({-bx:.3f} {-by:.3f})">{mark_body(spec, colors)}</g>', bw * scale


def lockup(primary, colors, word_color):
    """Horizontal lockup. The mark is as tall as the ascender of the wordmark
    and sits on the baseline. The gap is 0.5 x-heights."""
    x_height = 100
    mark_height = ASCENDER
    baseline = mark_height

    mark, mark_width = placed_mark(primary, colors, 0, 0, mark_height)
    _, scale = text_group('wordmark', 0, 0, x_height, word_color)
    left, top, right, bottom = ink('wordmark', scale)
    word_x = mark_width + 0.5 * x_height - left
    word, _ = text_group('wordmark', word_x, baseline, x_height, word_color)

    return svg(f'0 0 {word_x + right:.3f} {baseline:.3f}', mark + word)


def stacked(primary, colors, word_color):
    """Stacked lockup. The mark is 2.4 x-heights tall, with 0.8 x-heights of
    space above the word."""
    x_height = 100
    mark_height = 2.4 * x_height

    _, scale = text_group('wordmark', 0, 0, x_height, word_color)
    left, top, right, bottom = ink('wordmark', scale)
    word_width = right - left

    bx, by, bw, bh = primary['box']
    mark_width = bw * mark_height / bh
    width = max(word_width, mark_width)

    mark, _ = placed_mark(primary, colors, (width - mark_width) / 2, 0, mark_height)
    baseline = mark_height + 0.8 * x_height - top
    word, _ = text_group('wordmark', (width - word_width) / 2 - left, baseline, x_height, word_color)

    return svg(f'0 0 {width:.3f} {baseline + bottom:.3f}', mark + word)


def wordmark(color):
    entry = TEXTS['wordmark']
    left, top, right, bottom = entry['bounds']

    return svg(f'{left} {top} {right - left} {bottom - top}', f'<path d="{entry["path"]}" fill="{color}"/>')


def squircle(size=100, power=5, steps=180):
    import math

    points = []

    for step in range(steps):
        t = 2 * math.pi * step / steps
        c, s = math.cos(t), math.sin(t)
        x = size / 2 + size / 2 * math.copysign(abs(c) ** (2 / power), c)
        y = size / 2 + size / 2 * math.copysign(abs(s) ** (2 / power), s)
        points.append(f'{x:.3f} {y:.3f}')

    return 'M' + 'L'.join(points) + 'Z'


TILES = {
    'paper': {'fill': PAPER, 'colors': ON_LIGHT, 'edge': (INK, 0.12)},
    'ink': {'fill': INK, 'colors': {'ink': PAPER, 'commit': ACCENT}, 'edge': None},
    'accent': {'fill': ACCENT, 'colors': {'ink': PAPER, 'commit': INK}, 'edge': None},
}


def app_icon(primary, tile, shape):
    """An app icon. The squircle shape is for documents and stores. The square
    shape is for platforms that cut their own mask, such as iOS and Android.
    The mark stays inside the maskable safe zone in both shapes."""
    outline = squircle() if shape == 'squircle' else 'M0 0H100V100H0Z'
    body = f'<path d="{outline}" fill="{tile["fill"]}"/>'

    if tile['edge'] and shape == 'squircle':
        color, opacity = tile['edge']
        body += f'<path d="{outline}" fill="none" stroke="{color}" stroke-opacity="{opacity}" stroke-width=".6" transform="translate(.3 .3) scale(.994)"/>'

    body += f'<svg x="19" y="19" width="62" height="62" viewBox="0 0 64 64">{mark_body(primary, tile["colors"])}</svg>'

    return svg('0 0 100 100', body)


def small_box(small):
    side = max(small['box'][2], small['box'][3]) + 3

    return side, f'{32 - side / 2:.3f} {32 - side / 2:.3f} {side:.3f} {side:.3f}'


def favicon(small):
    """The favicon follows the light or dark theme of the browser."""
    style = (
        f'<style>.ink{{stroke:{INK}}}.commit{{stroke:{ACCENT}}}'
        f'@media (prefers-color-scheme:dark){{.ink{{stroke:{DARK_INK}}}}}</style>'
    )

    return svg(small_box(small)[1], style + mark_body(small, classes=True))


def favicon_tile(small):
    """A favicon on an ink tile, for PNG and ICO files that cannot follow the
    browser theme."""
    side = small_box(small)[0]
    inset = side * 0.06
    full = side + 2 * inset
    x0 = 32 - side / 2 - inset
    body = f'<rect x="{x0:.3f}" y="{x0:.3f}" width="{full:.3f}" height="{full:.3f}" rx="{full * 0.22:.3f}" fill="{INK}"/>' + mark_body(small, {'ink': PAPER, 'commit': ACCENT})

    return svg(f'{x0:.3f} {x0:.3f} {full:.3f} {full:.3f}', body)


def social(primary):
    """The GitHub social preview, 1280 by 640, on paper."""
    x_height = 84
    mark_height = ASCENDER * x_height / X_HEIGHT
    baseline = 330

    _, scale = text_group('wordmark', 0, 0, x_height, INK)
    left, top, right, bottom = ink('wordmark', scale)

    bx, by, bw, bh = primary['box']
    mark_width = bw * mark_height / bh
    total = mark_width + 0.5 * x_height + (right - left)
    x = (1280 - total) / 2

    mark, _ = placed_mark(primary, ON_LIGHT, x, baseline - mark_height, mark_height)
    word, _ = text_group('wordmark', x + mark_width + 0.5 * x_height - left, baseline, x_height, INK)

    tag_height = 20
    _, tag_scale = text_group('tagline', 0, 0, tag_height, MUTED)
    tag_left, _, tag_right, _ = ink('tagline', tag_scale)
    tagline, _ = text_group('tagline', (1280 - (tag_right - tag_left)) / 2 - tag_left, baseline + 96, tag_height, MUTED)

    rule = f'<rect x="0" y="624" width="1280" height="16" fill="{ACCENT}"/>'
    frame = f'<rect x=".5" y=".5" width="1279" height="639" fill="{PAPER}" stroke="{LINE}"/>'

    return svg('0 0 1280 640', frame + mark + word + tagline + rule)


def main():
    SVG.mkdir(exist_ok=True)

    primary = layout(PRIMARY)
    small = layout(SMALL)
    box = small_box(small)[1]

    files = {
        'mark-on-dark.svg': svg('0 0 64 64', mark_body(primary, ON_DARK)),
        'mark-on-light.svg': svg('0 0 64 64', mark_body(primary, ON_LIGHT)),
        'mark-white.svg': svg('0 0 64 64', mark_body(primary, {'ink': PAPER, 'commit': PAPER})),
        'mark-ink.svg': svg('0 0 64 64', mark_body(primary, {'ink': INK, 'commit': INK})),
        'mark-small-on-dark.svg': svg(box, mark_body(small, ON_DARK)),
        'mark-small-on-light.svg': svg(box, mark_body(small, ON_LIGHT)),
        'favicon.svg': favicon(small),
        'favicon-tile.svg': favicon_tile(small),
        'wordmark-on-dark.svg': wordmark(DARK_INK),
        'wordmark-on-light.svg': wordmark(INK),
        'lockup-on-dark.svg': lockup(primary, ON_DARK, DARK_INK),
        'lockup-on-light.svg': lockup(primary, ON_LIGHT, INK),
        'lockup-stacked-on-dark.svg': stacked(primary, ON_DARK, DARK_INK),
        'lockup-stacked-on-light.svg': stacked(primary, ON_LIGHT, INK),
        'social-preview.svg': social(primary),
    }

    for name, tile in TILES.items():
        files[f'app-icon-{name}.svg'] = app_icon(primary, tile, 'squircle')

    files['app-icon-square.svg'] = app_icon(primary, TILES['ink'], 'square')

    for name, content in files.items():
        (SVG / name).write_text(content)

    print(f'{len(files)} files in {SVG}')


if __name__ == '__main__':
    main()
