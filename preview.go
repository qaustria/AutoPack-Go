package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

const (
	previewSlotSize = 96
	previewGap      = 8
	previewMargin   = 16
	previewItemSize = 72
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
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open hotbar preview texture %q: %w", key, err)
		}
		texture, decodeErr := png.Decode(file)
		closeErr := file.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode hotbar preview texture %q: %w", key, decodeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close hotbar preview texture %q: %w", key, closeErr)
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
