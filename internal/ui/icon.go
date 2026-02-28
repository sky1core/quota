package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// GenIcon generates a 22x22 vertical bar level icon filled to pct%.
func GenIcon(pct int) []byte {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	const size = 22
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	black := color.RGBA{0, 0, 0, 255}

	// Bar area: x=6..15, y=1..20
	x0, x1 := 6, 15
	y0, y1 := 1, 20

	// Border (1px)
	for x := x0; x <= x1; x++ {
		img.Set(x, y0, black)
		img.Set(x, y1, black)
	}
	for y := y0; y <= y1; y++ {
		img.Set(x0, y, black)
		img.Set(x1, y, black)
	}

	// Fill from bottom
	innerTop := y0 + 2
	innerBot := y1 - 2
	innerH := innerBot - innerTop + 1
	fillRows := innerH * pct / 100
	fillTop := innerBot - fillRows + 1

	for y := fillTop; y <= innerBot; y++ {
		for x := x0 + 2; x <= x1-2; x++ {
			img.Set(x, y, black)
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
