package tests

import (
	"image"
	"image/color"
	"math"
	"testing"

	. "autopack/utils"
)

func TestFullGridGreedyMeshAndBlenderDimensions(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 16), G: uint8(y * 16), B: 127, A: 255})
		}
	}
	mesh, stats, err := BuildGreedyMesh(img, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if stats.OpaqueCells != 256 {
		t.Fatalf("opaque cells = %d, want 256", stats.OpaqueCells)
	}
	if stats.Quads != 6 {
		t.Fatalf("quads = %d, want a single box (6)", stats.Quads)
	}
	if got, want := len(mesh.Positions)/3, 24; got != want {
		t.Fatalf("vertices = %d, want %d", got, want)
	}
	if got, want := len(mesh.Indices), 36; got != want {
		t.Fatalf("indices = %d, want %d", got, want)
	}
	wantXZ := 2 * math.Sqrt(2)
	assertNear(t, stats.BlenderDimensions[0], wantXZ, 1e-12)
	assertNear(t, stats.BlenderDimensions[1], 0.07, 1e-12)
	assertNear(t, stats.BlenderDimensions[2], wantXZ, 1e-12)
}

func TestSingleCellPreservesSourceSizingAndCentering(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	img.SetNRGBA(3, 9, color.NRGBA{R: 255, A: 255})
	mesh, stats, err := BuildGreedyMesh(img, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Quads != 6 {
		t.Fatalf("quads = %d, want 6", stats.Quads)
	}
	wantXZ := 0.125 * math.Sqrt(2)
	assertNear(t, stats.BlenderDimensions[0], wantXZ, 1e-12)
	assertNear(t, stats.BlenderDimensions[1], 0.07, 1e-12)
	assertNear(t, stats.BlenderDimensions[2], wantXZ, 1e-12)
	// Centering is performed before the one-sided solidify thickness, just as in
	// the Python source. The local XY silhouette center therefore lands at zero.
	var minX, maxX float32 = mesh.Positions[0], mesh.Positions[0]
	for i := 3; i < len(mesh.Positions); i += 3 {
		if mesh.Positions[i] < minX {
			minX = mesh.Positions[i]
		}
		if mesh.Positions[i] > maxX {
			maxX = mesh.Positions[i]
		}
	}
	assertNear(t, float64(minX+maxX), 0, 1e-6)
}

func TestGreedyMeshRemovesInternalFaces(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	// A 3-cell L has 18 naive cube quads. Its closed union needs four front/back
	// rectangles and six straight silhouette runs: ten quads.
	for _, p := range [][2]int{{0, 0}, {1, 0}, {0, 1}} {
		img.SetNRGBA(p[0], p[1], color.NRGBA{A: 255})
	}
	cfg := DefaultConfig()
	_, stats, err := BuildGreedyMesh(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Quads != 10 {
		t.Fatalf("quads = %d, want 10", stats.Quads)
	}
}

func TestAlphaThreshold(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{A: 1})
	cfg := DefaultConfig()
	cfg.AlphaThreshold = 1
	if _, _, err := BuildGreedyMesh(img, cfg); err == nil {
		t.Fatal("expected fully transparent result to fail")
	}
	if _, _, err := BuildGreedyMesh(img, Config{PlaneSize: -1, Thickness: 0.07}); err == nil {
		t.Fatal("expected a negative plane size to fail")
	}
}

func TestDefaultMeshGeneratorNeverMeshesLowAlphaBackground(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	// Reproduce trauma.zip's broken canvas: almost every background pixel has
	// alpha 2 instead of alpha 0. The item itself is a small opaque silhouette.
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 2})
		}
	}
	for y := 24; y < 104; y++ {
		for x := 58; x < 70; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 120, G: 45, B: 20, A: 255})
		}
	}

	mesh, stats, err := BuildGreedyMesh(img, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if stats.OpaqueCells != 12*80 {
		t.Fatalf("opaque cells = %d, want only the %d item pixels", stats.OpaqueCells, 12*80)
	}
	for vertex := 0; vertex < len(mesh.TexCoords); vertex += 2 {
		u := mesh.TexCoords[vertex]
		v := mesh.TexCoords[vertex+1]
		if u <= 0 || u >= 1 || v <= 0 || v >= 1 {
			t.Fatalf("background reached mesh UVs at (%g, %g)", u, v)
		}
	}
}

func TestDetectBackgroundAlphaNoiseThresholdRejectsNoisyFullPlane(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 4})
		}
	}
	for y := 5; y < 11; y++ {
		for x := 7; x < 9; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}

	threshold := DetectBackgroundAlphaNoiseThreshold(img)
	if threshold != 4 {
		t.Fatalf("detected alpha threshold = %d, want 4", threshold)
	}
	cfg := DefaultConfig()
	cfg.AlphaThreshold = threshold
	_, stats, err := BuildGreedyMesh(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.OpaqueCells != 12 {
		t.Fatalf("opaque cells = %d, want only 12 real item pixels", stats.OpaqueCells)
	}
}

func TestDetectBackgroundAlphaNoiseThresholdPreservesNormalAntialiasing(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	img.SetNRGBA(7, 7, color.NRGBA{A: 4})
	img.SetNRGBA(8, 7, color.NRGBA{A: 255})
	if threshold := DetectBackgroundAlphaNoiseThreshold(img); threshold != 0 {
		t.Fatalf("normal sprite threshold = %d, want 0", threshold)
	}
}

func TestNonSquareImageKeepsAspectRatio(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	_, stats, err := BuildGreedyMesh(img, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if stats.GridWidth != 4 || stats.GridHeight != 2 {
		t.Fatalf("sampling = %dx%d, want 4x2", stats.GridWidth, stats.GridHeight)
	}
	if stats.Quads != 6 {
		t.Fatalf("quads = %d, want a single rectangular box (6)", stats.Quads)
	}
	wantXZ := 3 / math.Sqrt(2) // 2 units wide and 1 unit high after -45 degrees.
	assertNear(t, stats.BlenderDimensions[0], wantXZ, 1e-12)
	assertNear(t, stats.BlenderDimensions[2], wantXZ, 1e-12)
}

func TestUsesEveryPixelAt512(t *testing.T) {
	const size = 512
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-size/2, y-size/2
			distanceSquared := dx*dx + dy*dy
			alpha := uint8(0)
			if distanceSquared <= 180*180 {
				alpha = 255
			} else if distanceSquared <= 181*181 {
				alpha = 128
			}
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 180, A: alpha})
		}
	}
	mesh, stats, err := BuildGreedyMesh(img, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if stats.GridWidth != size || stats.GridHeight != size {
		t.Fatalf("sampling = %dx%d, want %dx%d", stats.GridWidth, stats.GridHeight, size, size)
	}
	if !mesh.AlphaBlend {
		t.Fatal("partial-alpha edge did not enable GLB alpha blending")
	}
	if stats.Quads >= stats.OpaqueCells {
		t.Fatalf("greedy meshing did not reduce the 512px silhouette: %d quads for %d cells", stats.Quads, stats.OpaqueCells)
	}
}

func assertNear(t *testing.T, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("got %.12g, want %.12g (tolerance %.3g)", got, want, tolerance)
	}
}
