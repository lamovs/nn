package capture

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/bits"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/lamovs/nn/internal/vault"
)

func gradient(seed, scale int) image.Image {
	w, h := 64*scale, 48*scale
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(int64(seed)))
	noise := make([]int, 64)
	for i := range noise {
		noise[i] = rng.Intn(120)
	}
	for y := range h {
		for x := range w {
			cell := (y/scale/6)*8 + x/scale/8
			v := (x/scale*3 + y/scale*2 + noise[cell%64]) % 256
			img.Set(x, y, color.RGBA{uint8(v), uint8(255 - v), uint8(noise[cell%64]), 255})
		}
	}
	return img
}

func flatImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for y := range 30 {
		for x := range 40 {
			img.Set(x, y, color.RGBA{250, 250, 250, 255})
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDetectImage(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		ext  string
	}{
		{"png", []byte("\x89PNG\r\n\x1a\nrest"), "png"},
		{"jpg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}, "jpg"},
		{"gif87", []byte("GIF87a...."), "gif"},
		{"gif89", []byte("GIF89a...."), "gif"},
		{"webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 "), "webp"},
		{"riff but not webp", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), ""},
		{"text", []byte("hello there"), ""},
		{"empty", nil, ""},
		{"short", []byte("RIFF"), ""},
	}
	for _, tt := range tests {
		ext, ok := DetectImage(tt.data)
		if ext != tt.ext || ok != (tt.ext != "") {
			t.Errorf("%s: DetectImage = %q, %v; want %q", tt.name, ext, ok, tt.ext)
		}
	}
	if ext, _ := DetectImage(encodePNG(t, gradient(1, 1))); ext != "png" {
		t.Errorf("real png = %q", ext)
	}
}

func TestDHash(t *testing.T) {
	base := gradient(7, 1)
	h1, ok1 := dHash(base)
	h2, ok2 := dHash(decode(t, encodeJPEG(t, base)))
	h3, ok3 := dHash(gradient(7, 3))
	other, ok4 := dHash(gradient(99, 1))
	if !ok1 || !ok2 || !ok3 || !ok4 {
		t.Fatalf("dHash refused an image: %v %v %v %v", ok1, ok2, ok3, ok4)
	}
	if d := bits.OnesCount64(h1 ^ h2); d > maxHamming {
		t.Errorf("jpeg re-encode distance = %d", d)
	}
	if d := bits.OnesCount64(h1 ^ h3); d > maxHamming {
		t.Errorf("scaled copy distance = %d", d)
	}
	if d := bits.OnesCount64(h1 ^ other); d <= maxHamming {
		t.Errorf("different image distance = %d", d)
	}
	if _, ok := dHash(flatImage()); ok {
		t.Error("flat image hashed")
	}
	if _, ok := dHash(image.NewRGBA(image.Rect(0, 0, 0, 0))); ok {
		t.Error("empty image hashed")
	}
	if _, ok := dHash(gradient(7, 1).(*image.RGBA).SubImage(image.Rect(0, 0, 3, 2))); ok {
		// A 3x2 crop has fewer pixels than cells; it must not panic.
		t.Log("tiny image hashed")
	}
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestDuplicateImage(t *testing.T) {
	root := t.TempDir()
	v := &vault.Vault{Root: root, Inbox: "nn"}
	assets := filepath.Join(root, "nn", "assets")
	if err := os.MkdirAll(filepath.Join(assets, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	shot := encodePNG(t, gradient(3, 1))
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(assets, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("shot.png", shot)
	write("other.png", encodePNG(t, gradient(42, 1)))
	write("notes.txt", []byte("not an image"))

	if rel, ok := DuplicateImage(v, shot); !ok || rel != "nn/assets/shot.png" {
		t.Errorf("exact copy = %q, %v", rel, ok)
	}
	if rel, ok := DuplicateImage(v, encodeJPEG(t, gradient(3, 1))); !ok || rel != "nn/assets/shot.png" {
		t.Errorf("re-encoded copy = %q, %v", rel, ok)
	}
	if rel, ok := DuplicateImage(v, encodePNG(t, gradient(3, 2))); !ok || rel != "nn/assets/shot.png" {
		t.Errorf("scaled copy = %q, %v", rel, ok)
	}
	if rel, ok := DuplicateImage(v, encodePNG(t, gradient(500, 1))); ok {
		t.Errorf("unrelated image matched %q", rel)
	}
	if rel, ok := DuplicateImage(v, encodePNG(t, flatImage())); ok {
		t.Errorf("flat image matched %q", rel)
	}
	if rel, ok := DuplicateImage(v, []byte("RIFF\x24\x00\x00\x00WEBPnot really")); ok {
		t.Errorf("undecodable image matched %q", rel)
	}
	if rel, ok := DuplicateImage(v, nil); ok {
		t.Errorf("empty data matched %q", rel)
	}
	if rel, ok := DuplicateImage(nil, shot); ok {
		t.Errorf("nil vault matched %q", rel)
	}

	// An exact copy found deeper in assets is still reported.
	nested := filepath.Join(assets, "old", "archived.png")
	deep := encodePNG(t, gradient(11, 1))
	if err := os.WriteFile(nested, deep, 0o644); err != nil {
		t.Fatal(err)
	}
	if rel, ok := DuplicateImage(v, deep); !ok || rel != "nn/assets/old/archived.png" {
		t.Errorf("nested copy = %q, %v", rel, ok)
	}

	empty := &vault.Vault{Root: t.TempDir(), Inbox: "nn"}
	if rel, ok := DuplicateImage(empty, shot); ok {
		t.Errorf("empty vault matched %q", rel)
	}
}
