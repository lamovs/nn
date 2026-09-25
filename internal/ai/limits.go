package ai

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// Request limits bound both the transport and the data handed to an engine.
const (
	MaxImageBytes     = 7 << 19
	MaxImageDimension = 8000
	MaxSystemBytes    = 64 << 10
	MaxTextBytes      = 256 << 10
	maxArgumentBytes  = (128 << 10) - 1
	maxArgumentsBytes = 192 << 10
)

func ValidateRequest(req Request) error {
	if len(req.System) > MaxSystemBytes {
		return fmt.Errorf("ai: system prompt exceeds %d bytes", MaxSystemBytes)
	}
	if len(req.Text) > MaxTextBytes {
		return fmt.Errorf("ai: text exceeds %d bytes", MaxTextBytes)
	}
	if len(req.Image) > MaxImageBytes {
		return fmt.Errorf("ai: image exceeds %d bytes", MaxImageBytes)
	}
	if len(req.Image) == 0 {
		return nil
	}
	kind, ok := sniffImage(req.Image)
	if !ok {
		return fmt.Errorf("ai: the image is not PNG, JPEG, GIF or WebP")
	}
	if kind.ext == "png" || kind.ext == "jpg" {
		return ValidateShotImage(req.Image)
	}
	return nil
}

// ValidateShotImage checks header dimensions before decoding.
func ValidateShotImage(data []byte) error {
	if len(data) > MaxImageBytes {
		return fmt.Errorf("ai: image exceeds %d bytes", MaxImageBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return fmt.Errorf("ai: screenshot must be a valid PNG or JPEG")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxImageDimension || cfg.Height > MaxImageDimension {
		return fmt.Errorf("ai: image dimensions exceed %d pixels", MaxImageDimension)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("ai: invalid screenshot: %w", err)
	}
	return nil
}

func validateArguments(args []string) error {
	total := 0
	for _, arg := range args {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("ai: command argument contains NUL")
		}
		if len(arg) > maxArgumentBytes {
			return fmt.Errorf("ai: command argument exceeds %d bytes; pass the prompt on stdin", maxArgumentBytes)
		}
		total += len(arg) + 1
		if total > maxArgumentsBytes {
			return fmt.Errorf("ai: command arguments exceed %d bytes", maxArgumentsBytes)
		}
	}
	return nil
}
