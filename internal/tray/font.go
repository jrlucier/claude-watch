package tray

import (
	"image"
	"image/color"
	"log"
	"os"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// systemSansFonts is the list of candidate regular-weight sans-serif faces
// we read at runtime. The first one that exists wins. These are read
// straight from the system fontconfig install paths — never redistributed
// by us, so license terms (OFL etc.) stay with the OS package that put them
// there.
//
// We prefer a neutral grotesque (Liberation / Arial) over a humanist
// (DejaVu) for tight panel-scale digits where stylistic flourishes turn
// into noise after downsampling.
var systemSansFonts = []string{
	"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
	"/usr/share/fonts/truetype/msttcorefonts/Arial.ttf",
	"/usr/share/fonts/TTF/LiberationSans-Regular.ttf",
	"/usr/share/fonts/liberation-sans/LiberationSans-Regular.ttf",
	"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
	"/usr/share/fonts/noto/NotoSans-Regular.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/TTF/DejaVuSans.ttf",
	"/usr/share/fonts/dejavu/DejaVuSans.ttf",
	"/Library/Fonts/Arial.ttf",
	"/System/Library/Fonts/Supplemental/Arial.ttf",
}

var (
	fontOnce sync.Once
	fontTTF  *sfnt.Font
	fontErr  error
)

func loadFont() (*sfnt.Font, error) {
	fontOnce.Do(func() {
		// Try each system font in order. First successful parse wins.
		for _, path := range systemSansFonts {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			f, err := opentype.Parse(data)
			if err != nil {
				continue
			}
			fontTTF = f
			return
		}
		// No system sans-serif available — fall back to the bundled Go font.
		// Functional, just slightly more distinctive than a neutral grotesque.
		f, err := opentype.Parse(goregular.TTF)
		if err != nil {
			fontErr = err
			log.Printf("warn: parse embedded font: %v", err)
			return
		}
		fontTTF = f
	})
	return fontTTF, fontErr
}

// drawText renders s onto img starting at horizontal pixel x, vertically
// centered on the canvas. Anti-aliased rasterization — at panel scale this
// looks like real text, not the blocky bitmap font we had before.
//
// If outline is non-transparent, a 1-pixel stroke in that color is painted
// around each glyph first — keeps the white digits legible on light panels.
func drawText(img *image.NRGBA, s string, size float64, x int, c color.Color, outline color.Color) {
	drawTextOffsetY(img, s, size, x, 0, c, outline)
}

// drawTextOffsetY is drawText with an additional vertical offset (positive =
// down, negative = up) applied after the centre calculation. Used when the
// caller has reserved a strip of canvas for some other element and wants the
// digit centered against the *remaining* area.
func drawTextOffsetY(img *image.NRGBA, s string, size float64, x, yOffset int, c color.Color, outline color.Color) {
	f, err := loadFont()
	if err != nil || f == nil {
		return
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		log.Printf("warn: opentype face: %v", err)
		return
	}
	defer face.Close()

	metrics := face.Metrics()
	// Vertically center on cap-height. Digits align to cap height so this
	// matches the visual centre of the glyph block.
	cap := metrics.CapHeight.Round()
	baseY := (img.Bounds().Dy()+cap)/2 - 2 + yOffset // -2: small optical lift

	// Outline first: paint the text at all 8 single-pixel offsets in the
	// outline color, then the main color on top. The 8-offset pattern gives
	// a uniform stroke (cardinals + diagonals).
	if _, _, _, a := outline.RGBA(); a > 0 {
		ol := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(outline),
			Face: face,
		}
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				if dx == 0 && dy == 0 {
					continue
				}
				ol.Dot = fixed.Point26_6{X: fixed.I(x + dx), Y: fixed.I(baseY + dy)}
				ol.DrawString(s)
			}
		}
	}

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(baseY)},
	}
	d.DrawString(s)
}

// drawTextWidth measures the rendered width of s at the given font size,
// without actually drawing anything. Used to pre-compute layout positions.
func drawTextWidth(s string, size float64) int {
	f, err := loadFont()
	if err != nil || f == nil {
		return 0
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return 0
	}
	defer face.Close()
	return font.MeasureString(face, s).Round()
}

// fitFontSize returns the largest size in pts that renders s within maxW × maxH.
// Binary-search avoids per-string magic-number tables.
func fitFontSize(s string, maxW, maxH int) float64 {
	f, err := loadFont()
	if err != nil || f == nil {
		return float64(maxH)
	}
	lo, hi := 8.0, float64(maxH)*1.5
	for lo+0.5 < hi {
		mid := (lo + hi) / 2
		face, err := opentype.NewFace(f, &opentype.FaceOptions{
			Size:    mid,
			DPI:     72,
			Hinting: font.HintingFull,
		})
		if err != nil {
			break
		}
		metrics := face.Metrics()
		h := metrics.CapHeight.Round()
		w := font.MeasureString(face, s).Round()
		face.Close()
		if w <= maxW && h <= maxH {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}
