package utils

import (
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
)

const (
	// MaxTextureDimension rejects forged PNG headers before the decoder can
	// allocate an unreasonable image. The pixel cap is the primary memory
	// bound; the side cap also rejects pathological one-pixel-wide images.
	MaxTextureDimension = 8192
	MaxTexturePixels    = 16 * 1024 * 1024
)

// DecodeTexturePNG validates dimensions from the PNG header before decoding
// pixel storage. It is intended for untrusted texture-pack files.
func DecodeTexturePNG(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	config, err := png.DecodeConfig(file)
	if err != nil {
		return nil, fmt.Errorf("decode PNG header %q: %w", path, err)
	}
	if config.Width <= 0 || config.Height <= 0 ||
		config.Width > MaxTextureDimension || config.Height > MaxTextureDimension ||
		uint64(config.Width)*uint64(config.Height) > MaxTexturePixels {
		return nil, fmt.Errorf(
			"PNG %q is %dx%d; limit is %d pixels and %d pixels per side",
			path, config.Width, config.Height, MaxTexturePixels, MaxTextureDimension,
		)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind PNG %q: %w", path, err)
	}
	img, err := png.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode PNG %q: %w", path, err)
	}
	return img, nil
}
