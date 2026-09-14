// Package imageconv wraps libvips (via govips) to convert HEIC/HEIF images
// into web-friendly JPEGs for previewing.
package imageconv

import (
	"errors"
	"fmt"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"
)

// MaxPreviewEdge is the longest-edge cap for generated preview JPEGs.
// Most HEIC photos from phones are ~4000x3000; 2048 keeps previews crisp on
// retina displays without ballooning storage or transfer size.
const MaxPreviewEdge = 2048

// PreviewQuality is the JPEG quality used for generated previews.
const PreviewQuality = 85

var (
	startupOnce sync.Once
	startupErr  error
)

func ensureStarted() error {
	startupOnce.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				startupErr = fmt.Errorf("vips startup panicked: %v", r)
			}
		}()
		vips.LoggingSettings(nil, vips.LogLevelError)
		startupErr = vips.Startup(&vips.Config{
			ConcurrencyLevel: 1,
			MaxCacheFiles:    0,
			MaxCacheMem:      64 << 20,
			MaxCacheSize:     20,
		})
	})
	return startupErr
}

// HEICToJPEG decodes a HEIC/HEIF byte slice and returns a JPEG-encoded byte
// slice resized so its longest edge is at most MaxPreviewEdge.
func HEICToJPEG(heic []byte) ([]byte, error) {
	if len(heic) == 0 {
		return nil, errors.New("imageconv: empty input")
	}
	if err := ensureStarted(); err != nil {
		return nil, err
	}

	img, err := vips.NewImageFromBuffer(heic)
	if err != nil {
		return nil, fmt.Errorf("imageconv: decode heic: %w", err)
	}
	defer img.Close()

	// Honour EXIF orientation so portraits aren't sideways.
	if err := img.AutoRotate(); err != nil {
		return nil, fmt.Errorf("imageconv: autorotate: %w", err)
	}

	width, height := img.Width(), img.Height()
	longest := width
	if height > longest {
		longest = height
	}
	if longest > MaxPreviewEdge {
		scale := float64(MaxPreviewEdge) / float64(longest)
		if err := img.Resize(scale, vips.KernelLanczos3); err != nil {
			return nil, fmt.Errorf("imageconv: resize: %w", err)
		}
	}

	jpegParams := vips.NewJpegExportParams()
	jpegParams.Quality = PreviewQuality
	jpegParams.StripMetadata = true

	jpegBytes, _, err := img.ExportJpeg(jpegParams)
	if err != nil {
		return nil, fmt.Errorf("imageconv: encode jpeg: %w", err)
	}
	return jpegBytes, nil
}
