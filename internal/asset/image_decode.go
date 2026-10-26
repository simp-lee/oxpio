package asset

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// ErrImageLimit indicates that an image exceeds a shared compressed-size or
// decoded-size limit.
var ErrImageLimit = errors.New("image exceeds configured limits")

const (
	// MaxImageBytes bounds the compressed input retained and parsed by image
	// decoders.
	MaxImageBytes = 10 << 20
	// MaxImageDimension bounds each decoded image dimension.
	MaxImageDimension = 8192
	// MaxImagePixels bounds the total number of decoded pixels.
	MaxImagePixels = 16 << 20
)

// DecodeImageConfig validates the input and its dimensions without allocating
// the complete decoded image.
func DecodeImageConfig(data []byte) (image.Config, string, error) {
	if len(data) > MaxImageBytes {
		return image.Config{}, "", fmt.Errorf("%w: image input exceeds 10 MiB", ErrImageLimit)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, "", err
	}
	if config.Width <= 0 || config.Height <= 0 {
		return image.Config{}, "", fmt.Errorf("image dimensions must be positive")
	}
	if config.Width > MaxImageDimension || config.Height > MaxImageDimension {
		return image.Config{}, "", fmt.Errorf("%w: image dimensions exceed %d pixels", ErrImageLimit, MaxImageDimension)
	}
	if int64(config.Width)*int64(config.Height) > MaxImagePixels {
		return image.Config{}, "", fmt.Errorf("%w: image exceeds %d decoded pixels", ErrImageLimit, MaxImagePixels)
	}
	return config, format, nil
}

// DecodeImage validates the input and dimensions before fully decoding it.
func DecodeImage(data []byte) (image.Image, string, error) {
	if _, _, err := DecodeImageConfig(data); err != nil {
		return nil, "", err
	}
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	return decoded, format, nil
}
