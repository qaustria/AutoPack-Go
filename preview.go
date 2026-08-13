package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"

	"github.com/qaustria/AutoPack-Go/utils"
)

const (
	previewSlotSize = 96
	previewGap      = 8
	previewMargin   = 16
	previewItemSize = 72
	previewIDFooter = 32
)

var previewTextureKeys = []string{"wool_red", "diamond_pickaxe", "iron_sword", "apple_golden"}

func buildHotbarPreview(textures map[string]string) ([]byte, error) {
	width := previewMargin*2 + len(previewTextureKeys)*previewSlotSize + (len(previewTextureKeys)-1)*previewGap
	height := previewMargin*2 + previewSlotSize
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.NRGBA{R: 18, G: 18, B: 20, A: 255}}, image.Point{}, draw.Src)

	for index, key := range previewTextureKeys {
		path := textures[key]
		if path == "" {
			return nil, fmt.Errorf("hotbar preview texture %q is missing", key)
		}
		texture, err := utils.DecodeTexturePNG(path)
		if err != nil {
			return nil, fmt.Errorf("decode hotbar preview texture %q: %w", key, err)
		}

		x := previewMargin + index*(previewSlotSize+previewGap)
		slot := image.Rect(x, previewMargin, x+previewSlotSize, previewMargin+previewSlotSize)
		drawHotbarSlot(canvas, slot)
		item := resizeNearestPreview(texture, previewItemSize, previewItemSize)
		itemPoint := image.Pt(x+(previewSlotSize-previewItemSize)/2, previewMargin+(previewSlotSize-previewItemSize)/2)
		draw.Draw(canvas, image.Rectangle{Min: itemPoint, Max: itemPoint.Add(item.Bounds().Size())}, item, item.Bounds().Min, draw.Over)
	}

	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&output, canvas); err != nil {
		return nil, fmt.Errorf("encode hotbar preview: %w", err)
	}
	return output.Bytes(), nil
}

func drawHotbarSlot(canvas *image.NRGBA, slot image.Rectangle) {
	draw.Draw(canvas, slot, &image.Uniform{C: color.NRGBA{R: 43, G: 43, B: 47, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(slot.Min.X, slot.Min.Y, slot.Max.X, slot.Min.Y+4), &image.Uniform{C: color.NRGBA{R: 196, G: 196, B: 202, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(slot.Min.X, slot.Min.Y, slot.Min.X+4, slot.Max.Y), &image.Uniform{C: color.NRGBA{R: 196, G: 196, B: 202, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(slot.Min.X, slot.Max.Y-4, slot.Max.X, slot.Max.Y), &image.Uniform{C: color.NRGBA{R: 7, G: 7, B: 9, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(slot.Max.X-4, slot.Min.Y, slot.Max.X, slot.Max.Y), &image.Uniform{C: color.NRGBA{R: 7, G: 7, B: 9, A: 255}}, image.Point{}, draw.Src)
}

func resizeNearestPreview(source image.Image, width, height int) *image.NRGBA {
	output := image.NewNRGBA(image.Rect(0, 0, width, height))
	bounds := source.Bounds()
	for y := 0; y < height; y++ {
		sourceY := bounds.Min.Y + y*bounds.Dy()/height
		for x := 0; x < width; x++ {
			sourceX := bounds.Min.X + x*bounds.Dx()/width
			output.Set(x, y, source.At(sourceX, sourceY))
		}
	}
	return output
}

// addPackIDToPreview places the pack identity in the Discord image so the
// webhook message itself can contain only the copyable pack JSON. A tiny
// built-in bitmap font keeps the server binary self-contained.
func addPackIDToPreview(encoded []byte, packID string) ([]byte, error) {
	preview, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("decode hotbar preview: %w", err)
	}
	bounds := preview.Bounds()
	canvas := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()+previewIDFooter))
	draw.Draw(canvas, image.Rect(0, 0, bounds.Dx(), bounds.Dy()), preview, bounds.Min, draw.Src)
	footer := image.Rect(0, bounds.Dy(), canvas.Bounds().Dx(), canvas.Bounds().Dy())
	draw.Draw(canvas, footer, &image.Uniform{C: color.NRGBA{R: 18, G: 18, B: 20, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(0, bounds.Dy(), canvas.Bounds().Dx(), bounds.Dy()+2), &image.Uniform{C: color.NRGBA{R: 255, G: 122, B: 0, A: 255}}, image.Point{}, draw.Src)

	const scale = 2
	label := "PACK ID:"
	id := strings.ToUpper(strings.TrimSpace(packID))
	y := bounds.Dy() + 9
	x := previewMargin
	drawBitmapText(canvas, x, y, label, color.NRGBA{R: 255, G: 122, B: 0, A: 255}, scale)
	x += bitmapTextWidth(label, scale) + 8
	drawBitmapText(canvas, x, y, id, color.NRGBA{R: 244, G: 244, B: 245, A: 255}, scale)

	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&output, canvas); err != nil {
		return nil, fmt.Errorf("encode labeled hotbar preview: %w", err)
	}
	return output.Bytes(), nil
}

func bitmapTextWidth(text string, scale int) int {
	if len(text) == 0 {
		return 0
	}
	return len(text)*(5*scale+scale) - scale
}

func drawBitmapText(canvas *image.NRGBA, x, y int, text string, ink color.NRGBA, scale int) {
	for _, character := range []byte(text) {
		glyph, exists := previewGlyphs[character]
		if !exists {
			glyph = previewGlyphs['?']
		}
		for row, bits := range glyph {
			for column := 0; column < 5; column++ {
				if bits&(1<<uint(4-column)) == 0 {
					continue
				}
				pixel := image.Rect(
					x+column*scale, y+row*scale,
					x+(column+1)*scale, y+(row+1)*scale,
				)
				draw.Draw(canvas, pixel, &image.Uniform{C: ink}, image.Point{}, draw.Src)
			}
		}
		x += 6 * scale
	}
}

var previewGlyphs = map[byte][7]uint8{
	' ': {},
	':': {0, 0b00100, 0b00100, 0, 0b00100, 0b00100, 0},
	'?': {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0, 0b00100},
	'0': {0b01110, 0b10001, 0b10011, 0b10101, 0b11001, 0b10001, 0b01110},
	'1': {0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'2': {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0b01000, 0b11111},
	'3': {0b11110, 0b00001, 0b00001, 0b01110, 0b00001, 0b00001, 0b11110},
	'4': {0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010},
	'5': {0b11111, 0b10000, 0b10000, 0b11110, 0b00001, 0b00001, 0b11110},
	'6': {0b01110, 0b10000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110},
	'7': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000},
	'8': {0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110},
	'9': {0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00001, 0b01110},
	'A': {0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'B': {0b11110, 0b10001, 0b10001, 0b11110, 0b10001, 0b10001, 0b11110},
	'C': {0b01111, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b01111},
	'D': {0b11110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b11110},
	'E': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b11111},
	'F': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b10000},
	'I': {0b11111, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b11111},
	'K': {0b10001, 0b10010, 0b10100, 0b11000, 0b10100, 0b10010, 0b10001},
	'P': {0b11110, 0b10001, 0b10001, 0b11110, 0b10000, 0b10000, 0b10000},
}
