package ocr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
)

const (
	// darkMedianLuma: below this, tesseract reads light-on-dark text far worse.
	darkMedianLuma = 128
	// upscaleBelowPixels: below this (about 1080p), glyphs are usually too
	// small for tesseract until doubled.
	upscaleBelowPixels = 2_500_000
)

// prepared.png is nil when the original file can be used as is.
type prepared struct {
	png           []byte
	width, height int
}

func preprocess(data []byte) (prepared, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return prepared{}, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return prepared{}, errors.New("image has no pixels")
	}

	gray, translucent := toGray(img)
	dark := medianLuma(gray) < darkMedianLuma
	small := w*h < upscaleBelowPixels
	if !dark && !small && !translucent {
		return prepared{width: w, height: h}, nil
	}
	if dark {
		for i, v := range gray.Pix {
			gray.Pix[i] = 255 - v
		}
	}
	if small {
		gray = upscale2x(gray)
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, gray); err != nil {
		return prepared{}, err
	}
	return prepared{png: buf.Bytes(), width: w, height: h}, nil
}

// toGray blends any transparency onto white and reports whether it had any.
func toGray(img image.Image) (*image.Gray, bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewGray(image.Rect(0, 0, w, h))
	translucent := false

	switch src := img.(type) {
	case *image.Gray:
		for y := range h {
			copy(dst.Pix[y*dst.Stride:y*dst.Stride+w], src.Pix[src.PixOffset(b.Min.X, b.Min.Y+y):])
		}
	case *image.RGBA:
		for y := range h {
			i := src.PixOffset(b.Min.X, b.Min.Y+y)
			row := dst.Pix[y*dst.Stride : y*dst.Stride+w]
			for x := range row {
				p := src.Pix[i : i+4 : i+4]
				// Premultiplied: blending onto white adds the missing coverage.
				row[x] = uint8(min(255, luma(p[0], p[1], p[2])+255-int(p[3])))
				translucent = translucent || p[3] != 255
				i += 4
			}
		}
	case *image.NRGBA:
		for y := range h {
			i := src.PixOffset(b.Min.X, b.Min.Y+y)
			row := dst.Pix[y*dst.Stride : y*dst.Stride+w]
			for x := range row {
				p := src.Pix[i : i+4 : i+4]
				a := int(p[3])
				row[x] = uint8((luma(p[0], p[1], p[2])*a + 255*(255-a) + 127) / 255)
				translucent = translucent || a != 255
				i += 4
			}
		}
	case *image.YCbCr:
		for y := range h {
			row := dst.Pix[y*dst.Stride : y*dst.Stride+w]
			for x := range row {
				row[x] = src.Y[src.YOffset(b.Min.X+x, b.Min.Y+y)]
			}
		}
	default:
		for y := range h {
			row := dst.Pix[y*dst.Stride : y*dst.Stride+w]
			for x := range row {
				r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				c := color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), uint8(a >> 8)}
				row[x] = uint8(min(255, luma(c.R, c.G, c.B)+255-int(c.A)))
				translucent = translucent || c.A != 255
			}
		}
	}
	return dst, translucent
}

func luma(r, g, b uint8) int {
	return (299*int(r) + 587*int(g) + 114*int(b) + 500) / 1000
}

func medianLuma(img *image.Gray) int {
	var hist [256]int
	for _, v := range img.Pix {
		hist[v]++
	}
	half := (len(img.Pix) + 1) / 2
	seen := 0
	for v, n := range hist {
		seen += n
		if seen >= half {
			return v
		}
	}
	return 255
}

// upscale2x doubles an image with bilinear filtering.
func upscale2x(src *image.Gray) *image.Gray {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewGray(image.Rect(0, 0, 2*w, 2*h))
	at := func(x, y int) int { return int(src.Pix[y*src.Stride+x]) }
	for y := range 2 * h {
		ny, fy := neighbours(y, h)
		row := dst.Pix[y*dst.Stride : y*dst.Stride+2*w]
		for x := range row {
			nx, fx := neighbours(x, w)
			sum := 9*at(nx, ny) + 3*at(fx, ny) + 3*at(nx, fy) + at(fx, fy)
			row[x] = uint8((sum + 8) / 16)
		}
	}
	return dst
}

func neighbours(d, n int) (near, far int) {
	near = d / 2
	far = near + 1
	if d%2 == 0 {
		far = near - 1
	}
	return near, min(max(far, 0), n-1)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
