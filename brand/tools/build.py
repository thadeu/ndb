"""Builds the ndb brand SVG files from geometry.

    python3 brand/tools/build.py && brand/tools/render.sh

The mark is a mascot that is also the logo: a ramen bowl. "One line, one
noodle": the noodles are the lines of an NDJSON file, and the last noodle, the
committed line, is the accent color. The wordmark `ndb` and the supporting cast
(onigiri, caterpillar, scroll) are drawn from geometry too, so no file needs a
font. Only the tagline of the social preview is an outlined font text.
"""

import re
import json
import math
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
SVG = ROOT / 'svg'
TEXTS = json.loads((ROOT / 'tools' / 'wordmark.json').read_text())

INK = '#0B0B0F'
PAPER = '#FFFFFF'
ACCENT = '#F0047F'
MUTED = '#6B6B76'
LINE = '#E5E5E5'
NOODLE = '#F6D58E'

DARK_INK = '#F4F4F6'

ON_LIGHT = {'outline': INK, 'bowl': PAPER, 'noodle': NOODLE, 'accent': ACCENT, 'steam': '#C9C9D1', 'cheek': ACCENT, 'cheek_opacity': 0.45}
ON_DARK = {**ON_LIGHT, 'steam': '#B8B8C2', 'halo': PAPER}
ON_ACCENT = {**ON_LIGHT, 'accent': INK, 'steam': PAPER, 'cheek': PAPER, 'cheek_opacity': 0.6}

PRIMARY = {'outline': 5, 'steam': True, 'box': (6, 0, 188, 196), 'face': 1, 'dome': 'M28 112C28 76 60 56 100 56C140 56 172 76 172 112Z', 'lines': ((62, 76, 60, 2.2, 1.75), (52, 90, 74, 2.4, 2), (48, 103, 90, 2.4, 2)), 'line': 5.5, 'halo': 3}
SMALL = {'outline': 7, 'steam': False, 'box': (4, 44, 192, 156), 'face': 1.3, 'dome': 'M26 112C26 76 58 56 100 56C142 56 174 76 174 112Z', 'lines': ((60, 82, 56, 3, 1.5), (50, 99, 84, 3, 1.5)), 'line': 9, 'halo': 4}
TINY = {'outline': 8, 'steam': False, 'box': (2, 80, 196, 122), 'face': 1.45, 'dome': None, 'lines': ((52, 98, 96, 5, 1.5),), 'line': 13, 'halo': 4}

LETTER_GAPS = (9, 14)
LETTER_WIDTH = 116
OVERSHOOT = 2
MARK_BASELINE = 190.5
MARK_BOX_HEIGHT = 196
X_HEIGHT = 100
ASCENDER = 145
STEM = 35


def mono(color):
    """One color for everything: outlines and details only, no fills."""
    return {'outline': color, 'bowl': 'none', 'noodle': 'none', 'accent': color, 'steam': color, 'cheek': 'none', 'cheek_opacity': 1}


def face(cx, cy, scale, c):
    """The shared face: dot eyes with a highlight, cheeks, a small smile."""
    eye = c['outline']
    shine = c['bowl'] if c['bowl'] != 'none' else 'none'
    parts = [
        f'<circle cx="-18" cy="0" r="6.5" fill="{eye}"/>',
        f'<circle cx="18" cy="0" r="6.5" fill="{eye}"/>',
        f'<circle cx="-16" cy="-2.5" r="2.2" fill="{shine}"/>',
        f'<circle cx="20" cy="-2.5" r="2.2" fill="{shine}"/>',
        f'<ellipse cx="-31" cy="11" rx="8" ry="4.5" fill="{c["cheek"]}" opacity="{c["cheek_opacity"]}"/>',
        f'<ellipse cx="31" cy="11" rx="8" ry="4.5" fill="{c["cheek"]}" opacity="{c["cheek_opacity"]}"/>',
        f'<path d="M-6 8Q0 15 6 8" fill="none" stroke="{eye}" stroke-width="3.5" stroke-linecap="round"/>',
    ]

    return f'<g transform="translate({cx:g} {cy:g}) scale({scale:g})">{"".join(parts)}</g>'


def outline_attrs(c, width):
    return f'stroke="{c["outline"]}" stroke-width="{width:g}" stroke-linejoin="round" stroke-linecap="round"'


def wavy(x, y, length, amp, waves, samples=28):
    """A soft sine line with round caps: one noodle."""
    points = []

    for step in range(samples + 1):
        t = step / samples
        points.append(f'{x + length * t:.2f} {y + amp * math.sin(2 * math.pi * waves * t):.2f}')

    return 'M' + 'L'.join(points)


BOWL = 'M14 112H186C186 160 150 188 100 188C50 188 14 160 14 112Z'


def ramen(spec, c):
    """The ramen bowl on a 200 unit grid. Back to front: a halo on dark
    surfaces, steam, noodle dome, the wavy noodle lines, the bowl, the face."""
    width = spec['outline']
    out = []

    if c.get('halo'):
        extra = width + 2 * spec['halo']
        halo = f'fill="{c["halo"]}" stroke="{c["halo"]}" stroke-width="{extra:g}" stroke-linejoin="round"'

        if spec['dome']:
            out.append(f'<path d="{spec["dome"]}" {halo}/>')

        out.append(f'<path d="{BOWL}" {halo}/>')

    if spec['steam']:
        steam = 'M78 12q-6 7 0 14q6 7 0 14M100 6q-6 7 0 14q6 7 0 14M122 12q-6 7 0 14q6 7 0 14'
        out.append(f'<path d="{steam}" fill="none" stroke="{c["steam"]}" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"/>')

    if spec['dome']:
        out.append(f'<path d="{spec["dome"]}" fill="{c["noodle"]}" {outline_attrs(c, width)}/>')

    last = len(spec['lines']) - 1

    for index, (x, y, length, amp, waves) in enumerate(spec['lines']):
        color = c['accent'] if index == last else c['outline']
        out.append(f'<path d="{wavy(x, y, length, amp, waves)}" fill="none" stroke="{color}" stroke-width="{spec["line"]:g}" stroke-linecap="round" stroke-linejoin="round"/>')

    out.append(f'<path d="{BOWL}" fill="{c["bowl"]}" {outline_attrs(c, width)}/>')
    out.append(face(100, 148, spec['face'], c))

    return ''.join(out)


def onigiri(c):
    shape = 'M100 24C113 24 121 33 129 47L174 128C185 148 174 172 150 172H50C26 172 15 148 26 128L71 47C79 33 87 24 100 24Z'
    nori = c['outline']
    line = '#3A3A44' if c['outline'] == INK else c['outline']

    return (
        f'<clipPath id="shape"><path d="{shape}"/></clipPath>'
        f'<path d="{shape}" fill="{c["bowl"]}"/>'
        f'<g clip-path="url(#shape)"><rect x="58" y="122" width="84" height="60" fill="{nori}"/>'
        f'<path d="M72 138H122M72 150H108" stroke="{line}" stroke-width="5" stroke-linecap="round"/>'
        f'<path d="M72 162H128" stroke="{c["accent"]}" stroke-width="5" stroke-linecap="round"/></g>'
        f'<path d="{shape}" fill="none" {outline_attrs(c, 5)}/>'
        + face(100, 94, 1, c)
    )


def caterpillar(c):
    o = c['outline']

    return (
        f'<path d="M42 140v14M72 144v14M102 146v14" stroke="{o}" stroke-width="4.5" stroke-linecap="round"/>'
        f'<circle cx="34" cy="122" r="22" fill="{c["accent"]}" {outline_attrs(c, 5)}/>'
        f'<circle cx="66" cy="124" r="24" fill="{c["bowl"]}" {outline_attrs(c, 5)}/>'
        f'<circle cx="100" cy="122" r="25" fill="{c["bowl"]}" {outline_attrs(c, 5)}/>'
        f'<path d="M136 72q-8 -22 -22 -28M162 70q8 -22 24 -26" fill="none" stroke="{o}" stroke-width="4.5" stroke-linecap="round"/>'
        f'<circle cx="113" cy="43" r="6" fill="{o}"/><circle cx="187" cy="43" r="6" fill="{o}"/>'
        f'<circle cx="148" cy="108" r="40" fill="{c["bowl"]}" {outline_attrs(c, 5)}/>'
        + face(148, 106, 0.9, c)
    )


def scroll(c):
    o = c['outline']

    return (
        f'<rect x="46" y="44" width="108" height="114" fill="{c["bowl"]}" {outline_attrs(c, 5)}/>'
        f'<path d="M66 112H130M66 126H112" stroke="{o}" stroke-width="6" stroke-linecap="round"/>'
        f'<path d="M66 140H136" stroke="{c["accent"]}" stroke-width="6" stroke-linecap="round"/>'
        + face(100, 80, 1, c)
        + f'<rect x="32" y="28" width="136" height="26" rx="13" fill="{c["noodle"]}" {outline_attrs(c, 5)}/>'
        f'<rect x="32" y="150" width="136" height="26" rx="13" fill="{c["noodle"]}" {outline_attrs(c, 5)}/>'
    )


def svg(view_box, body, title='ndb'):
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{view_box}" role="img" aria-label="{title}"><title>{title}</title>{body}</svg>\n'


def box_string(box):
    return ' '.join(f'{value:.3f}' for value in box)


def capsule(x0, x1, top, bottom):
    """A stem with full round ends, as a filled path."""
    r = (x1 - x0) / 2

    return f'M{x0:g} {top + r:g}A{r:g} {r:g} 0 0 1 {x1:g} {top + r:g}V{bottom - r:g}A{r:g} {r:g} 0 0 1 {x0:g} {bottom - r:g}Z'


def foot(x0, x1, top, bottom):
    """A stem with a flat top (hidden inside an arch) and a round foot."""
    r = (x1 - x0) / 2

    return f'M{x0:g} {top:g}H{x1:g}V{bottom - r:g}A{r:g} {r:g} 0 0 1 {x0:g} {bottom - r:g}Z'


def ring(cx, cy, rx, ry, ix, iy):
    """A ring of ellipses: the outer one runs clockwise, the counter runs the
    other way, so the nonzero fill leaves the counter open."""
    return (
        f'M{cx - rx:g} {cy:g}A{rx:g} {ry:g} 0 1 1 {cx + rx:g} {cy:g}A{rx:g} {ry:g} 0 1 1 {cx - rx:g} {cy:g}Z'
        f'M{cx - ix:g} {cy:g}A{ix:g} {iy:g} 0 1 0 {cx + ix:g} {cy:g}A{ix:g} {iy:g} 0 1 0 {cx - ix:g} {cy:g}Z'
    )


def ndb_letters():
    """The three letters as filled paths, with the baseline at y = 0 and an
    x-height of 100 units. The vertical stem is 35 units (0.35 x-heights), a
    heavy weight; the horizontal parts are 30 units, a little lighter, so the
    counters stay open. Ends and feet are fully round, like the mascot. The
    bowls of d and b overshoot the baseline and the x-height by 2 units. The
    gaps are optical: a round side needs less room than a straight one."""
    gap_nd, gap_db = LETTER_GAPS
    over = OVERSHOOT
    width = LETTER_WIDTH
    half = width / 2
    mid = -X_HEIGHT / 2
    thin = 30

    n_x = 0
    d_x = n_x + width + gap_nd
    b_x = d_x + width + gap_db

    arch = (
        f'M{n_x:g} {mid:g}A{half:g} {X_HEIGHT / 2:g} 0 0 1 {n_x + width:g} {mid:g}'
        f'H{n_x + width - STEM:g}A{half - STEM:g} {X_HEIGHT / 2 - thin:g} 0 0 0 {n_x + STEM:g} {mid:g}Z'
    )
    bowl = lambda x: ring(x + half, mid, half, X_HEIGHT / 2 + over, half - STEM, X_HEIGHT / 2 + over - thin)

    n = capsule(n_x, n_x + STEM, -X_HEIGHT, 0) + arch + foot(n_x + width - STEM, n_x + width, mid, 0)
    d = bowl(d_x) + capsule(d_x + width - STEM, d_x + width, -ASCENDER, over)
    b = capsule(b_x, b_x + STEM, -ASCENDER, over) + bowl(b_x)

    return n + d + b, [0, -ASCENDER, b_x + width, over]


NDB_PATH, NDB_BOUNDS = ndb_letters()
TEXTS['wordmark'] = {'path': NDB_PATH, 'bounds': NDB_BOUNDS, 'x_height': X_HEIGHT}


def text_group(name, x, baseline, x_height, color):
    """Places an outlined text so that its x-height is x_height units. The
    wordmark is stroked (round ends); the tagline is a filled outline."""
    entry = TEXTS[name]
    scale = x_height / entry['x_height']
    place = f'transform="translate({x:.3f} {baseline:.3f}) scale({scale:.6f})"'

    paint = f'fill="{color}"'

    return f'<path {place} d="{entry["path"]}" {paint}/>', scale


def ink(name, scale):
    left, top, right, bottom = TEXTS[name]['bounds']

    return left * scale, top * scale, right * scale, bottom * scale


def placed_mark(spec, c, x, y, height):
    bx, by, bw, bh = spec['box']
    scale = height / bh

    return f'<g transform="translate({x:.3f} {y:.3f}) scale({scale:.6f}) translate({-bx:.3f} {-by:.3f})">{ramen(spec, c)}</g>', bw * scale


def lockup(c, word_color):
    """Horizontal lockup. The x-height of the wordmark is 0.47 of the mascot
    height, the wordmark sits on the visual baseline of the bowl, and the gap is
    0.2 of the mascot height. A small pad keeps the last letter inside."""
    height = float(MARK_BOX_HEIGHT)
    xh = 0.47 * height
    baseline = MARK_BASELINE - OVERSHOOT * xh / X_HEIGHT

    mark, mark_width = placed_mark(PRIMARY, c, 0, 0, height)
    _, scale = text_group('wordmark', 0, 0, xh, word_color)
    left, top, right, bottom = ink('wordmark', scale)
    word_x = mark_width + 0.2 * height - left
    word, _ = text_group('wordmark', word_x, baseline, xh, word_color)

    return svg(f'0 0 {word_x + right + 3:.3f} {height:.3f}', mark + word)


def stacked(c, word_color):
    """Stacked lockup. The mascot is 3 x-heights tall, with 0.5 x-heights of
    space above the word."""
    mark_height = 3 * X_HEIGHT

    _, scale = text_group('wordmark', 0, 0, X_HEIGHT, word_color)
    left, top, right, bottom = ink('wordmark', scale)
    word_width = right - left

    bx, by, bw, bh = PRIMARY['box']
    mark_width = bw * mark_height / bh
    width = max(word_width, mark_width) + 6

    mark, _ = placed_mark(PRIMARY, c, (width - mark_width) / 2, 0, mark_height)
    baseline = mark_height + 0.5 * X_HEIGHT - top
    word, _ = text_group('wordmark', (width - word_width) / 2 - left, baseline, X_HEIGHT, word_color)

    return svg(f'0 0 {width:.3f} {baseline + bottom + 2:.3f}', mark + word)


def wordmark(color):
    body, _ = text_group('wordmark', 0, 0, X_HEIGHT, color)
    left, top, right, bottom = NDB_BOUNDS

    return svg(f'{left - 2} {top - 2} {right - left + 4} {bottom - top + 4}', body.replace(' transform="translate(0.000 0.000) scale(1.000000)"', ''))


def squircle(size=100, power=5, steps=180):
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
    'ink': {'fill': INK, 'colors': ON_DARK, 'edge': None},
    'accent': {'fill': ACCENT, 'colors': ON_ACCENT, 'edge': None},
}


def app_icon(tile, shape):
    """An app icon. The squircle shape is for documents and stores. The square
    shape is for platforms that cut their own mask, such as iOS and Android.
    The mascot stays inside the maskable safe zone in both shapes."""
    outline = squircle() if shape == 'squircle' else 'M0 0H100V100H0Z'
    body = f'<path d="{outline}" fill="{tile["fill"]}"/>'

    if tile['edge'] and shape == 'squircle':
        color, opacity = tile['edge']
        body += f'<path d="{outline}" fill="none" stroke="{color}" stroke-opacity="{opacity}" stroke-width=".6" transform="translate(.3 .3) scale(.994)"/>'

    body += f'<svg x="15" y="15" width="70" height="70" viewBox="{box_string(PRIMARY["box"])}">{ramen(PRIMARY, tile["colors"])}</svg>'

    return svg('0 0 100 100', body)


def square_box(spec, pad=0.0):
    """A square view box that centers the box of the spec."""
    x, y, w, h = spec['box']
    side = max(w, h) * (1 + pad)

    return side, (x + w / 2 - side / 2, y + h / 2 - side / 2, side, side)


def small_mark(c, spec=SMALL):
    side, box = square_box(spec, 0.04)

    return svg(box_string(box), ramen(spec, c))


def favicon_tile():
    """A favicon on an ink tile, for PNG and ICO files."""
    side, (x0, y0, full, _) = square_box(TINY, 0.25)
    tile = f'<rect x="{x0:.3f}" y="{y0:.3f}" width="{full:.3f}" height="{full:.3f}" rx="{full * 0.22:.3f}" fill="{INK}"/>'

    return svg(box_string((x0, y0, full, full)), tile + ramen(TINY, ON_DARK))


def social():
    """The GitHub social preview, 1280 by 640, on paper."""
    height = 300.0
    xh = 0.47 * height
    top = 96
    baseline = top + height * MARK_BASELINE / MARK_BOX_HEIGHT - OVERSHOOT * xh / X_HEIGHT

    _, scale = text_group('wordmark', 0, 0, xh, INK)
    left, _, right, _ = ink('wordmark', scale)

    bx, by, bw, bh = PRIMARY['box']
    mark_width = bw * height / bh
    total = mark_width + 0.2 * height + (right - left)
    x = (1280 - total) / 2

    mark, _ = placed_mark(PRIMARY, ON_LIGHT, x, top, height)
    word, _ = text_group('wordmark', x + mark_width + 0.2 * height - left, baseline, xh, INK)

    tag_height = 21
    _, tag_scale = text_group('tagline', 0, 0, tag_height, MUTED)
    tag_left, _, tag_right, _ = ink('tagline', tag_scale)
    tagline, _ = text_group('tagline', (1280 - (tag_right - tag_left)) / 2 - tag_left, top + height + 70, tag_height, MUTED)

    rule = f'<rect x="0" y="624" width="1280" height="16" fill="{ACCENT}"/>'
    frame = f'<rect x=".5" y=".5" width="1279" height="639" fill="{PAPER}" stroke="{LINE}"/>'

    return svg('0 0 1280 640', frame + mark + word + tagline + rule)


ANIMATION_CSS = """<style>
  /* part: the whole bowl bobs a little */
  .bob { animation: ndb-bob 3.2s ease-in-out infinite; transform-box: fill-box; transform-origin: center; }

  /* part: steam rises and fades, one wisp after the other */
  .steam { animation: ndb-steam 2.8s ease-in-out infinite; opacity: 0; }
  .steam-2 { animation-delay: 0.9s; }
  .steam-3 { animation-delay: 1.8s; }

  /* part: noodles sway, the magenta one last */
  .noodle { animation: ndb-sway 2.4s ease-in-out infinite; transform-box: fill-box; transform-origin: center; }
  .noodle-2 { animation-delay: 0.3s; }
  .noodle-3 { animation-delay: 0.6s; }

  /* part: eyes blink, cheeks glow */
  .eyes { animation: ndb-blink 4.6s infinite; transform-box: fill-box; transform-origin: center; }
  .cheek { animation: ndb-glow 3.2s ease-in-out infinite; }

  @keyframes ndb-bob { 0%, 100% { transform: translateY(0); } 50% { transform: translateY(-3px); } }
  @keyframes ndb-steam { 0% { opacity: 0; transform: translateY(8px); } 35% { opacity: 1; } 100% { opacity: 0; transform: translateY(-10px); } }
  @keyframes ndb-sway { 0%, 100% { transform: translateX(-2px); } 50% { transform: translateX(2px); } }
  @keyframes ndb-blink { 0%, 92%, 100% { transform: scaleY(1); } 95% { transform: scaleY(0.1); } }
  @keyframes ndb-glow { 0%, 100% { opacity: 0.45; } 50% { opacity: 0.8; } }

  @media (prefers-reduced-motion: reduce) {
    .bob, .steam, .noodle, .eyes, .cheek { animation: none; }
    .steam { opacity: 1; }
  }
</style>"""


def animated(mark):
    """The primary mark with CSS motion: steam, noodles, blink, bob.
    Used by the docs hero; static files stay static."""
    steam = re.search(r'<path d="(M78 12[^"]+)"([^>]*)/>', mark)
    wisps = re.findall(r'M\d+ \d+q[^M]+', steam.group(1))
    attrs = steam.group(2)
    steam_svg = ''.join(f'<path class="steam steam-{i + 1}" d="{w.strip()}"{attrs}/>' for i, w in enumerate(wisps))

    body = mark.replace(steam.group(0), '')
    head, rest = body.split('</title>', 1)
    rest = rest.rsplit('</svg>', 1)[0]

    for i in range(3):
        rest = re.sub(r'<path d="(M\d+\.00 \d+\.00L)', lambda m, n=i: f'<path class="noodle noodle-{n + 1}" d="{m.group(1)}', rest, count=1)

    rest = re.sub(
        r'(<circle cx="-18"[^>]*/><circle cx="18"[^>]*/><circle cx="-16"[^>]*/><circle cx="20"[^>]*/>)',
        r'<g class="eyes">\1</g>',
        rest,
    )

    rest = rest.replace('<ellipse ', '<ellipse class="cheek" ')

    return f'{head}</title>{ANIMATION_CSS}{steam_svg}<g class="bob">{rest}</g></svg>'


def main():
    SVG.mkdir(exist_ok=True)

    box = box_string(PRIMARY['box'])

    files = {
        'mark-on-dark.svg': svg(box, ramen(PRIMARY, ON_DARK)),
        'mark-on-light.svg': svg(box, ramen(PRIMARY, ON_LIGHT)),
        'mark-white.svg': svg(box, ramen(PRIMARY, mono(PAPER))),
        'mark-ink.svg': svg(box, ramen(PRIMARY, mono(INK))),
        'mark-small-on-dark.svg': small_mark(ON_DARK),
        'mark-small-on-light.svg': small_mark(ON_LIGHT),
        'favicon.svg': small_mark(ON_LIGHT, TINY),
        'favicon-tile.svg': favicon_tile(),
        'wordmark-on-dark.svg': wordmark(DARK_INK),
        'wordmark-on-light.svg': wordmark(INK),
        'lockup-on-dark.svg': lockup(ON_DARK, DARK_INK),
        'lockup-on-light.svg': lockup(ON_LIGHT, INK),
        'lockup-stacked-on-dark.svg': stacked(ON_DARK, DARK_INK),
        'lockup-stacked-on-light.svg': stacked(ON_LIGHT, INK),
        'social-preview.svg': social(),
    }

    for name, tile in TILES.items():
        files[f'app-icon-{name}.svg'] = app_icon(tile, 'squircle')

    files['app-icon-square.svg'] = app_icon(TILES['ink'], 'square')

    files['mark-animated-on-light.svg'] = animated(files['mark-on-light.svg'])
    files['mark-animated-on-dark.svg'] = animated(files['mark-on-dark.svg'])

    for name, content in files.items():
        (SVG / name).write_text(content)

    extras = SVG.parent / 'extras' / 'cast'
    extras.mkdir(parents=True, exist_ok=True)

    cast = {
        'onigiri': onigiri(ON_LIGHT),
        'caterpillar': caterpillar(ON_LIGHT),
        'scroll': scroll(ON_LIGHT),
    }

    for name, body in cast.items():
        (extras / f'cast-{name}.svg').write_text(svg('0 0 200 200', body, name))

    print(f'{len(files)} files in {SVG}')


if __name__ == '__main__':
    main()
