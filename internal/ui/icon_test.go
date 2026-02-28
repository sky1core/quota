package ui

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestGenIcon_ValidPNG(t *testing.T) {
	b := GenIcon(50)
	if len(b) == 0 {
		t.Fatal("empty icon")
	}
	// PNG magic bytes
	if b[0] != 0x89 || b[1] != 'P' || b[2] != 'N' || b[3] != 'G' {
		t.Error("not a valid PNG")
	}
}

func TestGenIcon_Bounds(t *testing.T) {
	for _, pct := range []int{-10, 0, 50, 100, 150} {
		b := GenIcon(pct)
		if len(b) == 0 {
			t.Errorf("empty icon for pct=%d", pct)
		}
	}
}

func TestGenIcon_DifferentFillLevels(t *testing.T) {
	// 0% should have no filled pixels in inner area, 100% should have all filled
	decode := func(pct int) image.Image {
		b := GenIcon(pct)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("png.Decode failed for pct=%d: %v", pct, err)
		}
		return img
	}

	countFilled := func(img image.Image) int {
		n := 0
		for y := 0; y < 22; y++ {
			for x := 0; x < 22; x++ {
				r, g, b, a := img.At(x, y).RGBA()
				if a > 0 && r == 0 && g == 0 && b == 0 {
					n++
				}
			}
		}
		return n
	}

	fill0 := countFilled(decode(0))
	fill50 := countFilled(decode(50))
	fill100 := countFilled(decode(100))

	if fill50 <= fill0 {
		t.Errorf("50%% (%d pixels) should have more fill than 0%% (%d pixels)", fill50, fill0)
	}
	if fill100 <= fill50 {
		t.Errorf("100%% (%d pixels) should have more fill than 50%% (%d pixels)", fill100, fill50)
	}
}

func TestGenIcon_Size(t *testing.T) {
	b := GenIcon(50)
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != 22 || bounds.Dy() != 22 {
		t.Errorf("expected 22x22, got %dx%d", bounds.Dx(), bounds.Dy())
	}
}
