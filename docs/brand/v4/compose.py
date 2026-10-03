"""Compose the generated Hibernaut v4 exploration boards with Pillow."""
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont, ImageOps

ROOT = Path(__file__).resolve().parent
LABELS = [
    '01 dashes', '02 arcs', '03 lid', '04 dreaming',
    '05 bite', '06 deep', '07 accent moon', '08 outline',
    '09 crescent + dashes', '10 crescent + arcs',
    '11 crescent + single lid', '12 crescent + accent',
]
FONT = ImageFont.truetype('/System/Library/Fonts/Menlo.ttc', 12)

def board(paths, labels, columns, rows, tile_size, output):
    width, height = tile_size
    gap = 8
    canvas = Image.new('RGB', (columns * width + (columns + 1) * gap,
                              rows * height + (rows + 1) * gap), '#18181b')
    for index, (path, label) in enumerate(zip(paths, labels)):
        tile = ImageOps.fit(Image.open(path).convert('RGB'), tile_size,
                            method=Image.Resampling.LANCZOS)
        ImageDraw.Draw(tile).text((16, height - 28), label, font=FONT, fill='#71717a')
        canvas.paste(tile, (gap + (index % columns) * (width + gap),
                            gap + (index // columns) * (height + gap)))
    canvas.save(output)

if __name__ == '__main__':
    marks = sorted(ROOT.glob('[0-9][0-9]-*.png'))
    assert len(marks) == 12
    board(marks, LABELS, 4, 3, (512, 512), ROOT.parent / 'hibernaut-logo-moodboard-v4.png')
    lockups = [ROOT / f'lockup-{number:02}.png' for number in range(9, 13)]
    if all(path.exists() for path in lockups):
        board(lockups, ['lockup ' + label for label in LABELS[8:]], 2, 2,
              (768, 512), ROOT.parent / 'hibernaut-lockups-v4.png')
