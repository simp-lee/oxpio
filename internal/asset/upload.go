package asset

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"path"
	"strings"

	_ "golang.org/x/image/webp"
)

// ValidateUploadImage enforces the shared editor/Agent upload size, decoded
// format, and SVG reference boundaries before any source file is created.
func ValidateUploadImage(name string, data []byte) error {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".svg":
		if len(data) > 1<<20 {
			return fmt.Errorf("svg upload exceeds 1 MiB")
		}
		if err := ValidateLocalSVG(data); err != nil {
			return fmt.Errorf("invalid svg image: %w", err)
		}
	case ".png", ".jpg", ".jpeg", ".webp":
		if len(data) > 10<<20 {
			return fmt.Errorf("image upload exceeds 10 MiB")
		}
		_, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("invalid image data: %w", err)
		}
		if (ext == ".png" && format != "png") || ((ext == ".jpg" || ext == ".jpeg") && format != "jpeg") || (ext == ".webp" && format != "webp") {
			return fmt.Errorf("image content does not match extension")
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("invalid image data: %w", err)
		}
	default:
		return fmt.Errorf("only PNG, JPEG, WebP, or SVG uploads are supported")
	}
	return nil
}
