package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// iconHeight is the side length we render at. Kept close to common panel sizes
// (22-32px) on purpose: rendering at 128 and letting the panel downsample
// 5.8× washed out the anti-aliased strokes (every edge pixel became 50% gray,
// so "white" text read as gray). At 64 the panel only halves on a 22-pixel
// panel, and on a 32-pixel panel it's near-native.
const iconHeight = 64

// Severity bands match Haletran/claude-usage-extension extension.js:369-377.
type Severity int

const (
	SevGreen Severity = iota
	SevYellow
	SevOrange
	SevRed
	SevUnknown // no data — render in gray
)

// SeverityFor maps a utilization percentage to a severity band.
func SeverityFor(util float64) Severity {
	switch {
	case util >= 90:
		return SevRed
	case util >= 70:
		return SevOrange
	case util >= 40:
		return SevYellow
	default:
		return SevGreen
	}
}

var sevColor = map[Severity]color.NRGBA{
	SevGreen:   {0x22, 0xc5, 0x5e, 0xff},
	SevYellow:  {0xea, 0xb3, 0x08, 0xff},
	SevOrange:  {0xf9, 0x73, 0x16, 0xff},
	SevRed:     {0xef, 0x44, 0x44, 0xff},
	SevUnknown: {0x9c, 0x9c, 0x9c, 0xff},
}

// digitColor is the on-icon text color. White; severity is conveyed by the
// bar's fill length and color.
var digitColor = color.NRGBA{0xff, 0xff, 0xff, 0xff}

// digitOutline is painted as a 1px stroke around the white digits so they
// stay legible on light panels (where pure-white text would otherwise blend
// into the background). Translucent so it doesn't add visual weight on the
// far more common dark panels.
var digitOutline = color.NRGBA{0x00, 0x00, 0x00, 0xa8}

// trackColor is the default unfilled portion of the progress bar. Slate-400
// (L≈0.65) tilts brighter than a true midtone — system trays are dark on
// most desktops, so we trade a little contrast on light panels (where the
// coloured fill still reads) for a clearly-visible track on the common
// black panel.
var trackColor = color.NRGBA{0x94, 0xa3, 0xb8, 0xff}

// paceWarnTrack replaces trackColor on the 5h bar when pace is "hot": the
// remaining capacity is the part actually in jeopardy at the current burn
// rate, so colouring it electric blue says "this remaining budget is what
// you're about to burn through." Saturated blue is complementary to every
// severity colour (green/yellow/orange/red are all warm), so the fill/track
// boundary stays crisp at any utilization%.
var paceWarnTrack = color.NRGBA{0x0e, 0xa5, 0xe9, 0xff}

// RenderBars produces the tray icon: a big 5-hour percentage number in
// neutral text, with a colored severity bar underneath it. When labelMode is
// "both" we also paint a top bar carrying the 7-day severity color — so the
// icon shows both signals at a glance whenever the panel label is showing
// both percentages too.
//
//	fiveH:     5h utilization percentage 0-100 (clamped).
//	sevenD:    7d utilization percentage 0-100 (drives the top bar in "both" mode).
//	stale:     when true, the digit & bars dim to indicate the API hasn't refreshed.
//	hasAPI:    when false, the icon renders "?" in gray — there's no value to show.
//	labelMode: "5h" (bottom bar only) or "both" (bottom + top bars).
//	paceHot:   when true, an amber triangle sits in the top-right corner of the icon
//	           — the "burning hot" pace flag. Suppressed by stale/!hasAPI since
//	           pace classification is meaningless without fresh data.
func RenderBars(fiveH, sevenD float64, stale, hasAPI bool, labelMode string, paceHot bool) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, iconHeight, iconHeight))

	var label string
	var sev Severity
	var sev7 Severity
	if !hasAPI {
		label = "?"
		sev = SevUnknown
		sev7 = SevUnknown
	} else {
		pct := int(fiveH + 0.5)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		label = pctLabel(pct)
		sev = SeverityFor(fiveH)
		sev7 = SeverityFor(sevenD)
	}

	textCol := digitColor
	barCol := sevColor[sev]
	bar7Col := sevColor[sev7]
	// Dimming signals "data is old". When we have no data at all the icon
	// already shows "?" — dimming on top would just make the glyph hard to
	// read. So only dim when hasAPI && stale.
	if stale && hasAPI {
		textCol = dim(textCol)
		barCol = dim(barCol)
		bar7Col = dim(bar7Col)
	}

	// Layout constants. The bars sit at the top and/or bottom; the digit
	// occupies the middle band.
	const lineH = 8       // bar thickness, px
	const lineMargin = 6  // px clear on left and right of each bar
	const lineEdgePad = 3 // px between a bar and the nearest canvas edge
	const topGap = 4      // extra breathing room between top bar and digit

	showTop := labelMode == "both"

	// Lock the font size to what's right for a 2-digit value ("88" is the
	// widest pair). Single-digit and "?" then render at the same height as
	// a 2-digit reading. Available digit height shrinks when we also have a
	// top bar competing for vertical space.
	ref := label
	if len(label) < 2 {
		ref = "88"
	}
	reserved := lineH + lineEdgePad + 2 // bottom bar + padding
	if showTop {
		reserved += lineH + lineEdgePad + topGap
	}
	digitW := iconHeight - 4
	digitH := iconHeight - reserved
	// 0.92× shaves a couple of pixels off the max-fit height so the digits
	// don't push right up against the icon edges; it reads as more
	// considered than "as big as possible."
	size := fitFontSize(ref, digitW, digitH) * 0.92
	outline := digitOutline
	if stale && hasAPI {
		outline = dim(outline)
	}
	textW := drawTextWidth(label, size)
	textX := (iconHeight - textW) / 2
	// Shift the visual centre of the text against the *remaining* canvas
	// (not the whole canvas), so the digit reads as centered between the
	// bars rather than drifting toward the line(s).
	yOffset := -(lineH + lineEdgePad) / 2
	if showTop {
		// Push the digit down so there's deliberate space between the top
		// bar and the cap line — otherwise the digit clings to the top bar
		// in a way that reads as cramped.
		yOffset = topGap / 2
	}
	drawTextOffsetY(img, label, size, textX, yOffset, textCol, outline)

	// Bars are progress bars: filled portion = utilization% in severity colour,
	// remaining portion = slate track. When pace is "hot", the 5h bar's track
	// turns pink — colouring the *remaining* capacity is the point, since
	// that's the budget the user is about to burn through. Top bar (7d) stays
	// on the neutral track; pace is a 5h concept.
	bottomTrack := trackColor
	if paceHot && hasAPI && !stale {
		bottomTrack = paceWarnTrack
	}
	if stale && hasAPI {
		bottomTrack = dim(bottomTrack)
	}
	bottomPct := fiveH
	if !hasAPI {
		bottomPct = 0
	}
	drawProgressBar(img, lineMargin, iconHeight-lineEdgePad-lineH, iconHeight-lineMargin, iconHeight-lineEdgePad, bottomPct, barCol, bottomTrack)
	if showTop {
		topTrack := trackColor
		if stale && hasAPI {
			topTrack = dim(topTrack)
		}
		topPct := sevenD
		if !hasAPI {
			topPct = 0
		}
		drawProgressBar(img, lineMargin, lineEdgePad, iconHeight-lineMargin, lineEdgePad+lineH, topPct, bar7Col, topTrack)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// pctLabel formats the percentage. 100 shows "!!" — two characters at the
// locked font size, matching the visual weight of 2-digit values, and
// "you're maxed out" is the only message that matters at saturation.
func pctLabel(p int) string {
	if p >= 100 {
		return "!!"
	}
	if p < 10 {
		return digit(p)
	}
	return digit(p/10) + digit(p%10)
}

func digit(n int) string { return string(rune('0' + n)) }

// drawProgressBar paints a horizontal bar split into a fill segment (left,
// length proportional to pct) and a track segment (right, the remainder).
// The outer corners get a 1-pixel chamfer for a "rounded" feel at panel
// scale; the fill/track boundary is left sharp so the proportion stays
// readable. pct is clamped to 0-100.
func drawProgressBar(img *image.NRGBA, x0, y0, x1, y1 int, pct float64, fill, track color.NRGBA) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	width := x1 - x0
	fillEnd := x0 + int(float64(width)*pct/100+0.5)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			leftCorner := x == x0 && (y == y0 || y == y1-1)
			rightCorner := x == x1-1 && (y == y0 || y == y1-1)
			if leftCorner || rightCorner {
				continue
			}
			c := track
			if x < fillEnd {
				c = fill
			}
			blend(img, x, y, c)
		}
	}
}

// drawDot paints an anti-aliased filled circle of radius r centered at (cx,cy)
// using source-over blending.
func drawDot(img *image.NRGBA, cx, cy, r int, c color.NRGBA) {
	for y := cy - r - 1; y <= cy+r+1; y++ {
		for x := cx - r - 1; x <= cx+r+1; x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			d := math.Sqrt(dx*dx + dy*dy)
			if d > float64(r)+0.5 {
				continue
			}
			alpha := c.A
			if d > float64(r)-0.5 {
				// Anti-aliased edge ring.
				alpha = uint8(float64(c.A) * (float64(r) + 0.5 - d))
			}
			ac := c
			ac.A = alpha
			blend(img, x, y, ac)
		}
	}
}

// blend writes the given NRGBA pixel into img with source-over compositing.
func blend(img *image.NRGBA, x, y int, c color.NRGBA) {
	b := img.Bounds()
	if x < b.Min.X || y < b.Min.Y || x >= b.Max.X || y >= b.Max.Y {
		return
	}
	i := (y-b.Min.Y)*img.Stride + (x-b.Min.X)*4
	dst := color.NRGBA{img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]}
	sa := float64(c.A) / 255
	da := float64(dst.A) / 255 * (1 - sa)
	outA := sa + da
	if outA <= 0 {
		return
	}
	out := func(s, d uint8) uint8 {
		v := (float64(s)*sa + float64(d)*da) / outA
		if v > 255 {
			v = 255
		}
		return uint8(v)
	}
	img.Pix[i+0] = out(c.R, dst.R)
	img.Pix[i+1] = out(c.G, dst.G)
	img.Pix[i+2] = out(c.B, dst.B)
	img.Pix[i+3] = uint8(outA * 255)
}

func dim(c color.NRGBA) color.NRGBA {
	return color.NRGBA{
		R: uint8(int(c.R) * 60 / 100),
		G: uint8(int(c.G) * 60 / 100),
		B: uint8(int(c.B) * 60 / 100),
		A: c.A,
	}
}
