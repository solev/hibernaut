# Hibernaut theme study

Recommendation: Glacier with Oxanium 600 for the wordmark and display text, paired with JetBrains Mono 400/500 for UI, body copy, and shell output.

## Artifacts

- [Ten-font comparison](wordmark-fonts.png), with [HTML source](wordmark-fonts.html).
- [Twelve-theme contact sheet](hibernaut-themes-v1.png), with [HTML source](themes.html).
- Glacier [preview](preview-glacier.png) and [HTML](preview-glacier.html).
- Lamplight [preview](preview-lamplight.png) and [HTML](preview-lamplight.html).
- Paper [preview](preview-paper.png) and [HTML](preview-paper.html).
- [Palette tokens and contrast measurements](palettes.json).
- [Rendered verification results](verification.json), including loaded font faces, overflow checks, exact sleeping-path counts, and the source SVG hash.

## Typography

The comparison renders Tomorrow, Saira, Oxanium, Chakra Petch, Exo 2, Rubik, Sora, Space Grotesk, Geist, and Lexend at 600 with the same size and tracking. Oxanium best matches the reference's flat-sided bowls and squared lowercase b, e, and a. Its corners have slight chamfers; it is a close font match, not a reconstruction of the supplied lettering. Saira is the softer runner-up. Tomorrow and Chakra Petch have more pronounced angular cuts. The remaining families have rounder bowls.

The final lockups use Oxanium's dotless i plus a separate square accent dot. JetBrains Mono gives commands, machine names, and timing values a readable, consistent rhythm.

Both selected fonts use the SIL Open Font License 1.1. Primary license sources: [Oxanium](https://github.com/google/fonts/blob/main/ofl/oxanium/OFL.txt) and [JetBrains Mono](https://github.com/JetBrains/JetBrainsMono/blob/master/OFL.txt). All comparison families are from Google Fonts' OFL directory.

Font binaries and downloaded licenses live in `/tmp/hibernaut-fonts`, outside the repository. HTML font declarations try that local cache first, then the original Google Fonts URL. After the cache is cleared, the HTML needs network access to load the fonts. The PNGs need no fonts or network.

## Ranked themes

1. Glacier. Near-black and ice blue connect the hibernation idea to a clear, restrained developer tool identity. The supplied candidate's blue i-dot carries through directly.
2. Lamplight. Warm charcoal and amber make the sleeping box feel at home in a late-night workspace. It has the most distinctive emotional direction.
3. Paper. Bone and ink preserve the restrained identity in daylight, with a darker ice-blue accent for readable controls. It is the strongest light option and a useful companion to Glacier.

## Contrast and geometry

Contrast uses WCAG relative luminance for opaque sRGB colors. Foreground/background ratios are 13.46–18.10:1. Accent/background ratios are 6.45–14.04:1. Every accent, foreground, and asleep text color also passes 4.5:1 against the theme's raised surface. Button text uses the background color on the accent fill. Full measurements are in `palettes.json`; background ratios appear on every contact-sheet tile. Light themes use darker blues because #7dd3fc does not provide text contrast on off-white.

Zinc mono's accent token is neutral gray. State text, filled/hollow indicators, and eye geometry distinguish awake and asleep without depending on hue.

Every sleeping mark in the HTML inlines the exact compound path from `../svg/hibernaut-mark.svg`, with only its fill recolored. The awake variant keeps the outer contour and replaces the dash cutouts with 6.8×6.8 rounded square cutouts with radius 1, centered at (29, 55.2) and (71, 55.2). Files in `docs/brand/svg` were not modified.

## Rendering

Headless Google Chrome rendered all artifacts at device scale factor 2. The font comparison uses a 1440×1190 layout and the contact sheet a 1920×1390 layout. Each preview uses a 1600×1000 layout, then macOS `sips` downsamples its 3200×2000 render to the requested 1600×1000 PNG. The HTML has responsive layouts for smaller screens.

These are brand previews. The install command follows the README's local-build workflow, and the timing note identifies the measured warm-resume setup. Fleet rows are sample data.
