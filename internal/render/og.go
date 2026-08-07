package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"strconv"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// OG image dimensions. 1200x630 is what every messenger and social preview expects.
const (
	OGWidth  = 1200
	OGHeight = 630
)

// OGOptions describes one link preview.
type OGOptions struct {
	// Days is the number shown large. It is the only thing guaranteed to render,
	// because digits are drawn geometrically rather than from a font.
	Days int
	// Title and Caption are drawn only when a font is available.
	Title   string
	Caption string
	// Accent is the background colour, as a hex string.
	Accent string
	// FontPath points at an optional TTF or OTF. Without it the image is the number
	// alone, which is still a perfectly good preview.
	FontPath string
}

// OG renders the link preview image as a PNG.
//
// No font is embedded. A font covering Latin, Cyrillic and CJK is tens of megabytes,
// and shipping a Latin-only one would render a Russian title as a row of boxes,
// which is worse than not drawing it. So the digits are drawn as geometry, which is
// script-independent, and any text is drawn only when the deployment mounts a font.
func OG(opts OGOptions) ([]byte, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, OGWidth, OGHeight))

	accent := parseHexColour(opts.Accent, color.RGBA{R: 0xe5, G: 0x68, B: 0x7f, A: 0xff})
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: accent}, image.Point{}, draw.Src)
	drawSoftGlow(canvas, accent)

	ink := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

	digits := strconv.Itoa(max(0, opts.Days))
	drawNumber(canvas, digits, ink)

	if opts.FontPath != "" {
		if err := drawCaptions(canvas, opts, ink); err != nil {
			// A missing or unreadable font must not fail the request: the number
			// carries the message on its own.
			return encodePNG(canvas)
		}
	}

	return encodePNG(canvas)
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("render: encode the preview image: %w", err)
	}
	return buf.Bytes(), nil
}

// drawSoftGlow lightens the upper area so the flat background has some depth.
func drawSoftGlow(canvas *image.RGBA, base color.RGBA) {
	bounds := canvas.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		// Fades from a light wash at the top to nothing by the middle.
		strength := 1 - float64(y)/float64(bounds.Dy())
		if strength < 0 {
			strength = 0
		}
		strength *= 0.28

		row := color.RGBA{
			R: blend(base.R, 0xff, strength),
			G: blend(base.G, 0xff, strength),
			B: blend(base.B, 0xff, strength),
			A: 0xff,
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			canvas.SetRGBA(x, y, row)
		}
	}
}

func blend(from, to uint8, amount float64) uint8 {
	return uint8(float64(from)*(1-amount) + float64(to)*amount)
}

// Geometry of the hand-drawn digits. Each digit is a seven-segment figure, which
// needs no font and reads identically in every language.
const (
	digitHeight  = 260
	digitWidth   = 150
	segmentThick = 30
	digitGap     = 34
)

// segments maps a digit to the seven segments it lights, in the usual order:
// top, top-left, top-right, middle, bottom-left, bottom-right, bottom.
var segments = map[rune][7]bool{
	'0': {true, true, true, false, true, true, true},
	'1': {false, false, true, false, false, true, false},
	'2': {true, false, true, true, true, false, true},
	'3': {true, false, true, true, false, true, true},
	'4': {false, true, true, true, false, true, false},
	'5': {true, true, false, true, false, true, true},
	'6': {true, true, false, true, true, true, true},
	'7': {true, false, true, false, false, true, false},
	'8': {true, true, true, true, true, true, true},
	'9': {true, true, true, true, false, true, true},
}

// advanceFor returns how much horizontal room a digit takes.
//
// A "1" lights only its right-hand segments, so giving it a full cell leaves a
// conspicuous hole to its left. Narrowing it is what proportional numerals do.
func advanceFor(digit rune) int {
	if digit == '1' {
		return segmentThick
	}
	return digitWidth
}

// drawNumber centres the digits on the canvas.
func drawNumber(canvas *image.RGBA, digits string, ink color.RGBA) {
	total := -digitGap
	for _, r := range digits {
		total += advanceFor(r) + digitGap
	}

	x := (OGWidth - total) / 2
	y := (OGHeight-digitHeight)/2 - 30

	for _, r := range digits {
		drawDigit(canvas, x, y, r, ink)
		x += advanceFor(r) + digitGap
	}
}

func drawDigit(canvas *image.RGBA, x, y int, digit rune, ink color.RGBA) {
	lit, ok := segments[digit]
	if !ok {
		return
	}

	// A "1" is drawn in its own narrow cell, so its right-hand segments sit where
	// the advance says rather than 150px away.
	width := digitWidth
	if digit == '1' {
		width = segmentThick
	}

	half := digitHeight / 2
	t := segmentThick

	// Segments deliberately overlap at the corners. Butting them up exactly leaves
	// visible notches where a horizontal meets a vertical.
	rects := [7]image.Rectangle{
		image.Rect(x, y, x+width, y+t),                            // top
		image.Rect(x, y, x+t, y+half+t/2),                         // top-left
		image.Rect(x+width-t, y, x+width, y+half+t/2),             // top-right
		image.Rect(x, y+half-t/2, x+width, y+half+t/2),            // middle
		image.Rect(x, y+half-t/2, x+t, y+digitHeight),             // bottom-left
		image.Rect(x+width-t, y+half-t/2, x+width, y+digitHeight), // bottom-right
		image.Rect(x, y+digitHeight-t, x+width, y+digitHeight),    // bottom
	}

	for i, on := range lit {
		if on {
			draw.Draw(canvas, rects[i], &image.Uniform{C: ink}, image.Point{}, draw.Over)
		}
	}
}

// drawCaptions renders the title and caption with a font supplied by the deployment.
func drawCaptions(canvas *image.RGBA, opts OGOptions, ink color.RGBA) error {
	body, err := os.ReadFile(opts.FontPath)
	if err != nil {
		return fmt.Errorf("render: read the preview font: %w", err)
	}
	parsed, err := opentype.Parse(body)
	if err != nil {
		return fmt.Errorf("render: parse the preview font: %w", err)
	}

	titleFace, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 56, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return fmt.Errorf("render: build the title face: %w", err)
	}
	defer func() { _ = titleFace.Close() }()

	captionFace, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 36, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return fmt.Errorf("render: build the caption face: %w", err)
	}
	defer func() { _ = captionFace.Close() }()

	if opts.Title != "" {
		drawCentred(canvas, titleFace, opts.Title, 110, ink)
	}
	if opts.Caption != "" {
		drawCentred(canvas, captionFace, opts.Caption, OGHeight-90, ink)
	}
	return nil
}

func drawCentred(canvas *image.RGBA, face font.Face, text string, baseline int, ink color.RGBA) {
	width := font.MeasureString(face, text)
	drawer := &font.Drawer{
		Dst:  canvas,
		Src:  &image.Uniform{C: ink},
		Face: face,
		Dot: fixed.Point26_6{
			X: fixed.I(OGWidth)/2 - width/2,
			Y: fixed.I(baseline),
		},
	}
	drawer.DrawString(text)
}

// parseHexColour reads #rgb or #rrggbb, falling back when the value is unusable.
func parseHexColour(s string, fallback color.RGBA) color.RGBA {
	if len(s) == 4 && s[0] == '#' {
		// Expand the short form: #abc means #aabbcc.
		s = string([]byte{'#', s[1], s[1], s[2], s[2], s[3], s[3]})
	}
	if len(s) != 7 || s[0] != '#' {
		return fallback
	}

	value, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return fallback
	}
	return color.RGBA{
		R: uint8(value >> 16),
		G: uint8(value >> 8),
		B: uint8(value),
		A: 0xff,
	}
}
