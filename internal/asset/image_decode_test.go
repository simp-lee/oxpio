package asset

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"testing"
)

func TestValidateUploadImageRejectsRasterLimits(t *testing.T) {
	onePixel := pngBytes(t, 1, 1)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "input bytes", data: append(append([]byte(nil), onePixel...), make([]byte, MaxImageBytes+1-len(onePixel))...)},
		{name: "width", data: pngBytes(t, MaxImageDimension+1, 1)},
		{name: "pixels", data: pngBytes(t, MaxImageDimension, MaxImagePixels/MaxImageDimension+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateUploadImage("image.png", test.data); err == nil {
				t.Fatal("ValidateUploadImage() accepted an oversized image")
			}
		})
	}
}

func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := append([]byte(nil), encoded.Bytes()...)
	binary.BigEndian.PutUint32(data[16:20], uint32(width))
	binary.BigEndian.PutUint32(data[20:24], uint32(height))
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	return data
}
