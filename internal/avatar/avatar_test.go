package avatar_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/XertroV/vecnet/internal/avatar"
)

func encPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func solid(w, h int, c color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// chunks lists a PNG's chunk types in order.
func chunks(t *testing.T, b []byte) []string {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("not a PNG: % x", b[:min(8, len(b))])
	}
	var out []string
	for p := 8; p+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[p:]))
		out = append(out, string(b[p+4:p+8]))
		p += 12 + n
	}
	return out
}

// checkOut asserts a normalised result: a Size×Size PNG of core chunks only
// whose hash is its name.
func checkOut(t *testing.T, r avatar.Result) image.Image {
	t.Helper()
	for _, c := range chunks(t, r.PNG) {
		if c != "IHDR" && c != "IDAT" && c != "IEND" {
			t.Fatalf("output carries chunk %q; want IHDR/IDAT/IEND only", c)
		}
	}
	img, err := png.Decode(bytes.NewReader(r.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != avatar.Size || b.Dy() != avatar.Size {
		t.Fatalf("output %v; want %dx%d", b, avatar.Size, avatar.Size)
	}
	if len(r.SHA256) != 64 {
		t.Fatalf("sha %q", r.SHA256)
	}
	return img
}

func near(c color.Color, r, g, b uint8, tol int) bool {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	cr, cg, cb := n.R, n.G, n.B
	d := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	return d(cr, r) <= tol && d(cg, g) <= tol && d(cb, b) <= tol
}

func TestNormalizeFormats(t *testing.T) {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, solid(300, 200, color.NRGBA{10, 200, 10, 255}), nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, format string
		data         []byte
	}{
		{"png", "png", encPNG(t, solid(64, 64, color.NRGBA{200, 10, 10, 255}))},
		{"jpeg", "jpeg", jpg.Bytes()},
		{"webp lossy", "webp", fixture(t, "photo.webp")},
		{"webp lossless alpha", "webp", fixture(t, "alpha.webp")},
		{"tiny upscales", "png", encPNG(t, solid(1, 1, color.NRGBA{1, 2, 3, 255}))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := avatar.Sniff(c.data); got != c.format {
				t.Fatalf("Sniff = %q; want %q", got, c.format)
			}
			r, err := avatar.Normalize(c.data)
			if err != nil {
				t.Fatal(err)
			}
			if r.Format != c.format {
				t.Fatalf("format %q", r.Format)
			}
			checkOut(t, r)
		})
	}
}

// A wide image keeps its centre: the crop drops the outer columns.
func TestNormalizeCenterCrops(t *testing.T) {
	img := solid(300, 100, color.NRGBA{255, 0, 0, 255})
	for y := 0; y < 100; y++ {
		for x := 100; x < 200; x++ {
			img.Set(x, y, color.NRGBA{0, 0, 255, 255})
		}
	}
	r, err := avatar.Normalize(encPNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	out := checkOut(t, r)
	for _, p := range []image.Point{{2, 2}, {64, 64}, {125, 125}} {
		if !near(out.At(p.X, p.Y), 0, 0, 255, 8) {
			t.Fatalf("pixel %v = %v; want the blue centre only", p, out.At(p.X, p.Y))
		}
	}
}

// WebP lossy fixture: left half red, right half blue, 96x64. The square crop
// is x 16..80, so both colours survive on their sides.
func TestNormalizeWebPPixels(t *testing.T) {
	r, err := avatar.Normalize(fixture(t, "photo.webp"))
	if err != nil {
		t.Fatal(err)
	}
	out := checkOut(t, r)
	if !near(out.At(10, 64), 220, 30, 30, 40) || !near(out.At(117, 64), 30, 30, 220, 40) {
		t.Fatalf("webp pixels %v %v", out.At(10, 64), out.At(117, 64))
	}
}

// A GIF yields its first frame.
func TestNormalizeGIFFirstFrame(t *testing.T) {
	frame := func(idx uint8) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, 32, 32), palette.Plan9)
		for i := range p.Pix {
			p.Pix[i] = idx
		}
		return p
	}
	ri := uint8(color.Palette(palette.Plan9).Index(color.RGBA{255, 0, 0, 255}))
	bi := uint8(color.Palette(palette.Plan9).Index(color.RGBA{0, 0, 255, 255}))
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{frame(ri), frame(bi)}, Delay: []int{10, 10}}); err != nil {
		t.Fatal(err)
	}
	r, err := avatar.Normalize(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	out := checkOut(t, r)
	want := palette.Plan9[ri].(color.RGBA)
	if !near(out.At(64, 64), want.R, want.G, want.B, 4) {
		t.Fatalf("gif pixel %v; want first frame %v", out.At(64, 64), want)
	}
}

// Transparency survives as transparency.
func TestNormalizeKeepsAlpha(t *testing.T) {
	r, err := avatar.Normalize(fixture(t, "alpha.webp"))
	if err != nil {
		t.Fatal(err)
	}
	out := checkOut(t, r)
	if _, _, _, a := out.At(1, 1).RGBA(); a != 0 {
		t.Fatalf("corner alpha %d; want 0", a)
	}
	if _, _, _, a := out.At(64, 64).RGBA(); a != 0xffff {
		t.Fatalf("centre alpha %d; want opaque", a)
	}
}

func TestNormalizeDeterministic(t *testing.T) {
	in := encPNG(t, solid(50, 70, color.NRGBA{9, 99, 199, 255}))
	a, err1 := avatar.Normalize(in)
	b, err2 := avatar.Normalize(in)
	if err1 != nil || err2 != nil || a.SHA256 != b.SHA256 || !bytes.Equal(a.PNG, b.PNG) {
		t.Fatalf("not deterministic: %v %v", err1, err2)
	}
}

// Security: EXIF (and every other metadata block) is gone after re-encode.
func TestNormalizeStripsEXIF(t *testing.T) {
	in := fixture(t, "exif.jpg")
	if !bytes.Contains(in, []byte("VECNET-EXIF-MARKER")) || !bytes.Contains(in, []byte("Exif")) {
		t.Fatal("fixture lost its EXIF")
	}
	r, err := avatar.Normalize(in)
	if err != nil {
		t.Fatal(err)
	}
	checkOut(t, r)
	for _, bad := range []string{"VECNET-EXIF-MARKER", "Exif", "eXIf", "tEXt", "iTXt", "zTXt"} {
		if bytes.Contains(r.PNG, []byte(bad)) {
			t.Fatalf("output still carries %q", bad)
		}
	}
}

// Security: PNG text chunks and bytes appended after IEND (a polyglot's
// payload) do not survive.
func TestNormalizeStripsPNGExtrasAndTrailer(t *testing.T) {
	base := encPNG(t, solid(16, 16, color.NRGBA{1, 1, 1, 255}))
	text := pngChunk("tEXt", []byte("Comment\x00VECNET-TEXT-MARKER"))
	in := append(append(append([]byte{}, base[:33]...), text...), base[33:]...) // after IHDR
	in = append(in, []byte("<script>alert('VECNET-TRAILER')</script>")...)
	r, err := avatar.Normalize(in)
	if err != nil {
		t.Fatal(err)
	}
	checkOut(t, r)
	if bytes.Contains(r.PNG, []byte("VECNET-")) || bytes.Contains(r.PNG, []byte("<script")) {
		t.Fatal("output carries input metadata or trailer")
	}
}

func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	binary.Write(&b, binary.BigEndian, crc.Sum32())
	return b.Bytes()
}

// pngHeader is a PNG declaring w×h, with one tiny (invalid) IDAT.
func pngHeader(w, h uint32) []byte { return pngHeaderDepth(w, h, 8) }

// pngHeaderDepth is pngHeader for an RGBA PNG of the given bit depth.
func pngHeaderDepth(w, h uint32, depth byte) []byte {
	var ihdr bytes.Buffer
	binary.Write(&ihdr, binary.BigEndian, w)
	binary.Write(&ihdr, binary.BigEndian, h)
	ihdr.Write([]byte{depth, 6, 0, 0, 0}) // RGBA
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, pngChunk("IHDR", ihdr.Bytes())...)
	out = append(out, pngChunk("IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})...)
	return append(out, pngChunk("IEND", nil)...)
}

// Security: a pixel bomb (huge declared canvas, tiny file) is refused from
// its header, before any decode.
func TestNormalizeRefusesPixelBombs(t *testing.T) {
	gifBomb := []byte("GIF89a\xff\xff\xff\xff\x00\x00\x00;") // 65535x65535 logical screen
	cases := map[string][]byte{
		"png 100000x100000": pngHeader(100000, 100000),
		"png 5000x5000":     pngHeader(5000, 5000), // over MaxPixels, under MaxSide
		"png 9000x1":        pngHeader(9000, 1),    // over MaxSide
		"gif 65535x65535":   gifBomb,
		// 16-bit RGBA decodes to 8 bytes a pixel: 9 MP is 72 MiB, over
		// MaxDecodedBytes though under MaxPixels.
		"png16 3000x3000": pngHeaderDepth(3000, 3000, 16),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if len(data) > 200 {
				t.Fatalf("bomb fixture is %d bytes; it should be tiny", len(data))
			}
			_, err := avatar.Normalize(data)
			if !errors.Is(err, avatar.ErrDimensions) {
				t.Fatalf("err = %v; want ErrDimensions", err)
			}
		})
	}
}

// The same 9 MP canvas at 8 bits a channel (36 MiB) passes the header
// checks; this one then fails to decode, as its IDAT is empty.
func TestNormalizeDecodedBudgetIsPerDepth(t *testing.T) {
	_, err := avatar.Normalize(pngHeaderDepth(3000, 3000, 8))
	if !errors.Is(err, avatar.ErrInvalid) {
		t.Fatalf("err = %v; want ErrInvalid (past the header checks)", err)
	}
}

// Security: SVG, HTML, other markup and random bytes are not images.
func TestNormalizeRefusesNonRaster(t *testing.T) {
	rnd := make([]byte, 4096)
	rand.New(rand.NewSource(7)).Read(rnd)
	rnd[0] = 'x'
	cases := map[string][]byte{
		"svg":         []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><script>alert(1)</script></svg>`),
		"svg xml":     []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`),
		"svg bom":     []byte("\xef\xbb\xbf<svg xmlns=\"http://www.w3.org/2000/svg\"/>"),
		"html":        []byte(`<!doctype html><img src=x onerror=alert(1)>`),
		"bmp":         append([]byte("BM"), make([]byte, 60)...),
		"tiff":        append([]byte("II*\x00"), make([]byte, 60)...),
		"random":      rnd,
		"empty":       {},
		"webp no hdr": []byte("RIFF\x00\x00\x00\x00WEBP"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := avatar.Normalize(data)
			if name == "webp no hdr" {
				if !errors.Is(err, avatar.ErrInvalid) {
					t.Fatalf("err = %v; want ErrInvalid", err)
				}
				return
			}
			if !errors.Is(err, avatar.ErrUnsupported) {
				t.Fatalf("err = %v; want ErrUnsupported", err)
			}
		})
	}
}

// Security: a polyglot that starts like an image but is not one is refused,
// by its declared size or when it fails to decode.
func TestNormalizeRefusesPolyglots(t *testing.T) {
	cases := map[string][]byte{
		"gifar":          []byte("GIF89a/*\x01\x00\x01\x00*/=1;<script>alert(1)</script>"), // declares 10799 wide
		"gif html":       []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00<script>alert(1)</script>"),
		"png garbage":    append([]byte("\x89PNG\r\n\x1a\n"), []byte(strings.Repeat("<html>", 100))...),
		"jpeg truncated": {0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'},
		"png bad idat":   pngHeader(64, 64),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := avatar.Normalize(data)
			if !errors.Is(err, avatar.ErrInvalid) && !errors.Is(err, avatar.ErrDimensions) {
				t.Fatalf("err = %v; want ErrInvalid or ErrDimensions", err)
			}
		})
	}
}

// Security: the byte cap is checked before anything else.
func TestNormalizeRefusesOversize(t *testing.T) {
	big := make([]byte, avatar.MaxBytes+1)
	copy(big, encPNG(t, solid(4, 4, color.Black)))
	if _, err := avatar.Normalize(big); !errors.Is(err, avatar.ErrTooLarge) {
		t.Fatalf("err = %v; want ErrTooLarge", err)
	}
}
