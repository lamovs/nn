// Package capture is the pure logic behind "nn add": image sniffing,
// duplicate detection, tag and note similarity, and secret scanning.
package capture

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"math"
	"math/bits"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lamovs/nn/internal/vault"
)

// ErrNotImplemented marks functionality this package only stubs out.
var ErrNotImplemented = errors.New("not implemented")

// DetectImage sniffs data's magic bytes for png, jpg, gif or webp.
func DetectImage(data []byte) (ext string, ok bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png", true
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "jpg", true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif", true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp", true
	}
	return "", false
}

// maxHamming is the largest dHash distance still counted as a duplicate.
const maxHamming = 4

// DuplicateImage reports whether data is already in the vault's assets, by
// exact sha256 match or by perceptual hash (dHash, hamming <= maxHamming).
func DuplicateImage(v *vault.Vault, data []byte) (rel string, ok bool) {
	if v == nil || len(data) == 0 {
		return "", false
	}
	assets := assetImages(v)

	sum := sha256.Sum256(data)
	for _, rel := range assets {
		info, err := os.Stat(v.Abs(rel))
		if err != nil || info.Size() != int64(len(data)) {
			continue
		}
		if existing, err := os.ReadFile(v.Abs(rel)); err == nil && sha256.Sum256(existing) == sum {
			return rel, true
		}
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", false
	}
	hash, ok := dHash(img)
	if !ok {
		return "", false
	}
	aspect := float64(img.Bounds().Dx()) / float64(img.Bounds().Dy())

	best, bestDist := "", maxHamming+1
	for _, rel := range assets {
		other, ok := decodeSimilarShape(v.Abs(rel), aspect)
		if !ok {
			continue
		}
		otherHash, ok := dHash(other)
		if !ok {
			continue
		}
		if d := bits.OnesCount64(hash ^ otherHash); d < bestDist {
			best, bestDist = rel, d
		}
	}
	return best, best != ""
}

func assetImages(v *vault.Vault) []string {
	dir := v.Abs(path.Join(v.Inbox, vault.AssetsDir))
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !vault.IsImage(d.Name()) {
			return nil
		}
		if rel, err := filepath.Rel(v.Root, p); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func decodeSimilarShape(path string, aspect float64) (image.Image, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width == 0 || cfg.Height == 0 {
		return nil, false
	}
	if other := float64(cfg.Width) / float64(cfg.Height); math.Abs(other-aspect) > 0.1*aspect {
		return nil, false
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, false
	}
	img, _, err := image.Decode(f)
	return img, err == nil
}

// dHash is a 64-bit difference hash over an 8x9 grid of gray cells; ok is
// false for images too flat for the bits to mean anything.
func dHash(img image.Image) (hash uint64, ok bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return 0, false
	}
	var cells [8][9]float64
	lo, hi := math.Inf(1), math.Inf(-1)
	for cy := range 8 {
		y0, y1 := span(b.Min.Y, h, 8, cy)
		for cx := range 9 {
			x0, x1 := span(b.Min.X, w, 9, cx)
			stepY, stepX := max(1, (y1-y0)/16), max(1, (x1-x0)/16)
			var sum float64
			n := 0
			for y := y0; y < y1; y += stepY {
				for x := x0; x < x1; x += stepX {
					r, g, bl, _ := img.At(x, y).RGBA()
					sum += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
					n++
				}
			}
			cell := sum / float64(n)
			cells[cy][cx] = cell
			lo, hi = math.Min(lo, cell), math.Max(hi, cell)
		}
	}
	if hi-lo < 0.02*0xffff {
		return 0, false
	}
	for cy := range 8 {
		for cx := range 8 {
			hash <<= 1
			if cells[cy][cx] > cells[cy][cx+1] {
				hash |= 1
			}
		}
	}
	return hash, true
}

func span(origin, length, n, i int) (int, int) {
	start := origin + i*length/n
	end := origin + (i+1)*length/n
	if end <= start {
		end = start + 1
	}
	if limit := origin + length; end > limit {
		start, end = limit-1, limit
	}
	return start, end
}
