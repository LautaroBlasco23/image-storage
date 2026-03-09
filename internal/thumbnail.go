package internal

import (
	"bytes"
	"image"

	"github.com/chai2010/webp"
	"github.com/nfnt/resize"
)

func generateThumbnail(img image.Image, size uint) ([]byte, error) {
	thumb := resize.Thumbnail(size, size, img, resize.Lanczos3)
	var buf bytes.Buffer
	if err := webp.Encode(&buf, thumb, &webp.Options{Lossless: false, Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
