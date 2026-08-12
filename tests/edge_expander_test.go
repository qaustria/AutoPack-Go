package tests

import (
	"image"
	"image/color"
	"math"
	"testing"

	. "autopack/utils"
)

func TestEdgeExpandUsesExactSizeDistanceAndFalloff(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	source.SetNRGBA(256, 256, color.NRGBA{R: 12, G: 34, B: 56, A: 255})
	result := EdgeExpand(source)
	if result.Bounds() != image.Rect(0, 0, 512, 512) {
		t.Fatalf("bounds = %v", result.Bounds())
	}
	for _, distance := range []int{0, 1, 24, 45, 46, 47, 48, 49} {
		pixel := result.NRGBAAt(256+distance, 256)
		wantAlpha := uint8(0)
		if distance <= 48 {
			fade := math.Max(0, float64(48-distance)/48)
			wantAlpha = uint8(fade*fade*255 + 0.5)
		}
		if pixel.A != wantAlpha {
			t.Fatalf("alpha at distance %d = %d, want %d", distance, pixel.A, wantAlpha)
		}
		if wantAlpha > 0 && (pixel.R != 12 || pixel.G != 34 || pixel.B != 56) {
			t.Fatalf("RGB at distance %d = %v", distance, pixel)
		}
	}
}

func TestEdgeExpandNearestResizeAndDoesNotMutateSource(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	source.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	source.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	original := append([]byte(nil), source.Pix...)
	result := EdgeExpand(source)
	if got := result.NRGBAAt(31, 0); got.R != 255 || got.A != 255 {
		t.Fatalf("last pixel of first nearest block = %v", got)
	}
	if got := result.NRGBAAt(32, 0); got.G != 255 || got.A != 255 {
		t.Fatalf("first pixel of second nearest block = %v", got)
	}
	for index := range original {
		if source.Pix[index] != original[index] {
			t.Fatalf("source changed at byte %d", index)
		}
	}
}

func TestEdgeExpandTieUsesPythonBFSSeedOrder(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	source.SetNRGBA(100, 200, color.NRGBA{R: 255, A: 255})
	source.SetNRGBA(104, 200, color.NRGBA{B: 255, A: 255})
	result := EdgeExpand(source)
	// At the equal-distance center, row-major source initialization reaches the
	// pixel from the left seed first, exactly like the Python deque.
	if got := result.NRGBAAt(102, 200); got.R != 255 || got.B != 0 {
		t.Fatalf("equal-distance source color = %v, want left/red seed", got)
	}
}
