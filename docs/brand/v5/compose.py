"""Compose v5 boards, exact v3 wordmark lockups, and favicon comparisons."""
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont

ROOT = Path(__file__).resolve().parent
BG = '#09090b'
FRAME = '#18181b'
FONT = ImageFont.truetype('/System/Library/Fonts/Menlo.ttc', 12)
LABELS = ['01 original refined', '02 squircle', '03 wide', '04 low eyes',
          '05 short eyes', '06 tight eyes', '07 outline', '08 inverse',
          '09 blue eyes', '10 awake pair', '11 fork', '12 app icon']
SELECTED = [1, 2, 4, 5]

def bounds(image, threshold=100):
    return image.convert('L').point(lambda value: 255 if value > threshold else 0).getbbox()

def foreground_alpha(image):
    alpha = Image.new('L', image.size)
    alpha.putdata([max(0, min(255, round((max(pixel) - 9) * 255 / 235)))
                   for pixel in image.get_flattened_data()])
    return alpha

def board(paths, labels, columns, tile_size, output):
    width, height = tile_size
    rows = (len(paths) + columns - 1) // columns
    canvas = Image.new('RGB', (columns * width + (columns + 1) * 8,
                              rows * height + (rows + 1) * 8), FRAME)
    for index, (path, label) in enumerate(zip(paths, labels)):
        tile = Image.open(path).convert('RGB').resize(tile_size, Image.Resampling.LANCZOS)
        ImageDraw.Draw(tile).text((16, height - 28), label, font=FONT, fill='#71717a')
        canvas.paste(tile, (8 + index % columns * (width + 8),
                            8 + index // columns * (height + 8)))
    canvas.save(output)

def lockups(paths):
    source = Image.open(ROOT.parent / 'v3/12-wordmark.png').convert('RGB')
    # Exclude the old v3 symbol; preserve all nine letterforms and the square i-dot.
    word = source.crop((250, 440, 910, 575))
    word = word.crop(bounds(word))
    word = word.resize((950, round(word.height * 950 / word.width)), Image.Resampling.LANCZOS)
    alpha = foreground_alpha(word)
    for number in SELECTED:
        mark = Image.open(paths[number - 1]).convert('RGB')
        mark = mark.crop(bounds(mark))
        # Ascender h begins five reference pixels below the top of the i-dot.
        ascender_height = round(word.height * 95 / 100)
        mark = mark.resize((round(mark.width * ascender_height / mark.height), ascender_height), Image.Resampling.LANCZOS)
        mark_alpha = foreground_alpha(mark)
        gap = 60
        total_width = mark.width + gap + word.width
        left = (1536 - total_width) // 2
        top = (1024 - word.height) // 2
        canvas = Image.new('RGB', (1536, 1024), BG)
        canvas.paste(mark, (left, top + word.height - mark.height), mark_alpha)
        canvas.paste(word, (left + mark.width + gap, top), alpha)
        canvas.save(ROOT / f'lockup-{number:02}.png')

def favicon_strip(paths):
    canvas = Image.new('RGB', (1536, 1344), BG)
    draw = ImageDraw.Draw(canvas)
    draw.text((24, 20), 'favicon test / cropped marks / lanczos downsample / nearest-neighbour enlargement', font=FONT, fill='#71717a')
    for row, number in enumerate(SELECTED):
        y = 164 + row * 312
        draw.text((24, y + 42), LABELS[number - 1], font=FONT, fill='#71717a')
        image = Image.open(paths[number - 1]).convert('RGB')
        image = image.crop(bounds(image))
        for column, size in enumerate([16, 32, 64]):
            x = 230 + column * 420
            draw.text((x, y - 112), f'{size}px / 1x + 4x', font=FONT, fill='#71717a')
            small = image.resize((size, size), Image.Resampling.LANCZOS)
            canvas.paste(small, (x, y + 48 - size // 2))
            enlarged = small.resize((size * 4, size * 4), Image.Resampling.NEAREST)
            canvas.paste(enlarged, (x + 90, y + 48 - enlarged.height // 2))
    canvas.save(ROOT.parent / 'hibernaut-favicon-test-v5.png')

if __name__ == '__main__':
    paths = sorted(ROOT.glob('[0-9][0-9]-*.png'))
    assert len(paths) == 12, len(paths)
    for path in paths:
        image = Image.open(path).convert('RGB')
        if image.size != (1024, 1024):
            image.resize((1024, 1024), Image.Resampling.LANCZOS).save(path)
    board(paths, LABELS, 4, (512, 512), ROOT.parent / 'hibernaut-logo-moodboard-v5.png')
    lockups(paths)
    board([ROOT / f'lockup-{number:02}.png' for number in SELECTED],
          [f'lockup {LABELS[number - 1]}' for number in SELECTED],
          2, (768, 512), ROOT.parent / 'hibernaut-lockups-v5.png')
    favicon_strip(paths)
