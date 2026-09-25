package ocr

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeGray(t *testing.T, data []byte) *image.Gray {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	g, ok := img.(*image.Gray)
	if !ok {
		t.Fatalf("preprocessed image is %T, want *image.Gray", img)
	}
	return g
}

func TestPreprocessInvertsDarkBackground(t *testing.T) {
	img := filled(2000, 1400, color.RGBA{30, 30, 30, 255})
	for x := 100; x < 400; x++ {
		img.Set(x, 700, color.White)
	}
	prep, err := preprocess(encodePNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if prep.png == nil {
		t.Fatal("dark image was not preprocessed")
	}
	g := decodeGray(t, prep.png)
	if g.Bounds().Dx() != 2000 {
		t.Errorf("large image was resized to %v", g.Bounds())
	}
	if bg, text := g.GrayAt(10, 10).Y, g.GrayAt(200, 700).Y; bg < 200 || text > 50 {
		t.Errorf("background %d, text %d: want light background with dark text", bg, text)
	}
}

func TestPreprocessDoublesSmallImage(t *testing.T) {
	img := filled(300, 100, color.White)
	img.Set(10, 10, color.Black)
	prep, err := preprocess(encodePNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if prep.width != 300 || prep.height != 100 {
		t.Errorf("original size = %dx%d", prep.width, prep.height)
	}
	g := decodeGray(t, prep.png)
	if g.Bounds() != image.Rect(0, 0, 600, 200) {
		t.Errorf("bounds = %v", g.Bounds())
	}
	if v := g.GrayAt(21, 21).Y; v > 128 {
		t.Errorf("upscaled dark pixel = %d", v)
	}
	if v := g.GrayAt(100, 100).Y; v != 255 {
		t.Errorf("upscaled white pixel = %d", v)
	}
}

func TestPreprocessLeavesLargeLightImageAlone(t *testing.T) {
	prep, err := preprocess(encodePNG(t, filled(2000, 1400, color.White)))
	if err != nil {
		t.Fatal(err)
	}
	if prep.png != nil || prep.width != 2000 || prep.height != 1400 {
		t.Errorf("prep = png:%v %dx%d", prep.png != nil, prep.width, prep.height)
	}
}

func TestPreprocessFlattensTransparencyOntoWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2000, 1400))
	img.SetNRGBA(5, 5, color.NRGBA{0, 0, 0, 255})
	prep, err := preprocess(encodePNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	if prep.png == nil {
		t.Fatal("transparent image was not flattened")
	}
	g := decodeGray(t, prep.png)
	if v := g.GrayAt(100, 100).Y; v != 255 {
		t.Errorf("transparent pixel = %d, want white", v)
	}
	if v := g.GrayAt(5, 5).Y; v != 0 {
		t.Errorf("opaque black pixel = %d", v)
	}
}

func TestPreprocessRejectsUnknownFormat(t *testing.T) {
	if _, err := preprocess([]byte("RIFF\x00\x00\x00\x00WEBPVP8 ")); err == nil {
		t.Error("want a decode error for webp")
	}
}
