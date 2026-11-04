// Package avatar turns an untrusted uploaded image into a peer avatar: a
// Size×Size PNG with no metadata (PROTOCOL §8.1). It is pure: bytes in,
// bytes out, no I/O.
//
// The checks run cheapest first, so hostile input costs little: the byte cap,
// then a refusal of anything that is not one of the four raster formats (SVG
// and other markup included), then image.DecodeConfig to read the declared
// dimensions, which must fit MaxSide and MaxPixels before a single pixel is
// decoded (a decompression bomb declares a huge canvas in a small file). Only
// then is the image decoded (a GIF's first frame only), center-cropped to a
// square, scaled with Catmull-Rom and re-encoded as PNG. Re-encoding drops
// EXIF, XMP, ICC profiles, text chunks and anything appended after the image,
// and makes the output deterministic, so its SHA-256 names it.
package avatar

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Limits.
const (
	// MaxBytes caps an upload: 2 MiB.
	MaxBytes = 2 << 20
	// MaxSide caps either declared dimension.
	MaxSide = 8192
	// MaxPixels caps the declared canvas: 16 MP (4096×4096).
	MaxPixels = 4096 * 4096
	// MaxDecodedBytes caps the memory one decoded image may take, from its
	// declared size and colour model: 64 MiB, so 16 MP at 4 bytes a pixel
	// but only 8 MP of 16-bit RGBA (8 bytes a pixel).
	MaxDecodedBytes = 64 << 20
	// Size is the side of every stored avatar, in pixels.
	Size = 128
)

// Errors. Each says what is wrong without echoing the input.
var (
	ErrTooLarge    = errors.New("avatar: image exceeds the byte cap")
	ErrUnsupported = errors.New("avatar: not a PNG, JPEG, GIF or WebP image")
	ErrDimensions  = errors.New("avatar: image dimensions exceed the pixel cap")
	ErrInvalid     = errors.New("avatar: image could not be decoded")
)

// Formats are the accepted input formats, as image.DecodeConfig names them.
var Formats = []string{"png", "jpeg", "gif", "webp"}

// decoder is one accepted format: its magic, and its own decode functions.
// Dispatching on our own table, not image.Decode's registry, means a format
// some other package registers (BMP, TIFF, …) is never accepted by accident.
type decoder struct {
	name   string
	magic  func([]byte) bool
	config func(*bytes.Reader) (image.Config, error)
	decode func(*bytes.Reader) (image.Image, error)
}

var decoders = []decoder{{
	name:   "png",
	magic:  prefix("\x89PNG\r\n\x1a\n"),
	config: func(r *bytes.Reader) (image.Config, error) { return png.DecodeConfig(r) },
	decode: func(r *bytes.Reader) (image.Image, error) { return png.Decode(r) },
}, {
	name:   "jpeg",
	magic:  prefix("\xff\xd8\xff"),
	config: func(r *bytes.Reader) (image.Config, error) { return jpeg.DecodeConfig(r) },
	decode: func(r *bytes.Reader) (image.Image, error) { return jpeg.Decode(r) },
}, {
	name:   "gif",
	magic:  func(b []byte) bool { return prefix("GIF87a")(b) || prefix("GIF89a")(b) },
	config: func(r *bytes.Reader) (image.Config, error) { return gif.DecodeConfig(r) },
	decode: func(r *bytes.Reader) (image.Image, error) { return gif.Decode(r) }, // the first frame only
}, {
	name:   "webp",
	magic:  func(b []byte) bool { return len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" },
	config: func(r *bytes.Reader) (image.Config, error) { return webp.DecodeConfig(r) },
	decode: func(r *bytes.Reader) (image.Image, error) { return webp.Decode(r) },
}}

func prefix(p string) func([]byte) bool {
	return func(b []byte) bool { return bytes.HasPrefix(b, []byte(p)) }
}

// Result is a normalised avatar.
type Result struct {
	PNG    []byte // Size×Size PNG, IHDR/IDAT/IEND only
	SHA256 string // lower-hex SHA-256 of PNG
	Format string // the input format: one of Formats
}

// Sniff reports the input format by its magic bytes, or "" when it is none
// of Formats. SVG, HTML and every other text format are "".
func Sniff(data []byte) string {
	for _, d := range decoders {
		if d.magic(data) {
			return d.name
		}
	}
	return ""
}

// Normalize checks data and returns it as a Size×Size PNG. Errors wrap one
// of ErrTooLarge, ErrUnsupported, ErrDimensions or ErrInvalid.
func Normalize(data []byte) (Result, error) {
	if len(data) > MaxBytes {
		return Result{}, ErrTooLarge
	}
	var dec *decoder
	for i := range decoders {
		if decoders[i].magic(data) {
			dec = &decoders[i]
			break
		}
	}
	if dec == nil {
		return Result{}, ErrUnsupported
	}
	cfg, err := dec.config(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s header", ErrInvalid, dec.name)
	}
	if cfg.Width < 1 || cfg.Height < 1 {
		return Result{}, fmt.Errorf("%w: empty %s canvas", ErrInvalid, dec.name)
	}
	px := int64(cfg.Width) * int64(cfg.Height)
	if cfg.Width > MaxSide || cfg.Height > MaxSide || px > MaxPixels || px*bytesPerPixel(cfg.ColorModel) > MaxDecodedBytes {
		return Result{}, fmt.Errorf("%w: %dx%d", ErrDimensions, cfg.Width, cfg.Height)
	}
	src, err := dec.decode(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s data", ErrInvalid, dec.name)
	}
	b := src.Bounds()
	// A decoder must not hand back more than it declared; refuse rather
	// than trust a frame that grew.
	if b.Dx() < 1 || b.Dy() < 1 || b.Dx() > MaxSide || b.Dy() > MaxSide || int64(b.Dx())*int64(b.Dy()) > MaxPixels {
		return Result{}, fmt.Errorf("%w: decoded bounds", ErrInvalid)
	}
	out, err := encode(scale(src, squareCrop(b)))
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(out)
	return Result{PNG: out, SHA256: hex.EncodeToString(sum[:]), Format: dec.name}, nil
}

// bytesPerPixel is what a decoder allocates per pixel for model: 8 for
// 16-bit colour, else at most 4 (RGBA, YCbCr, CMYK, gray, paletted).
func bytesPerPixel(m color.Model) int64 {
	switch m {
	case color.RGBA64Model, color.NRGBA64Model:
		return 8
	}
	return 4
}

// squareCrop is the largest centered square inside b.
func squareCrop(b image.Rectangle) image.Rectangle {
	side := min(b.Dx(), b.Dy())
	x := b.Min.X + (b.Dx()-side)/2
	y := b.Min.Y + (b.Dy()-side)/2
	return image.Rect(x, y, x+side, y+side)
}

// scale draws the crop of src onto a Size×Size canvas with Catmull-Rom, the
// sharpest of x/image/draw's kernels that does not ring badly on downscale.
// The canvas is premultiplied RGBA: x/image/draw's fast paths need it, and
// filtering premultiplied colour keeps transparent pixels from bleeding.
func scale(src image.Image, crop image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, Size, Size))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, xdraw.Src, nil)
	return dst
}

// encode writes img as PNG at best compression. Go's encoder writes only
// IHDR, IDAT and IEND for an RGBA image (RGB when it is opaque): no metadata
// can survive.
func encode(img *image.RGBA) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("avatar: encode: %w", err)
	}
	return buf.Bytes(), nil
}
