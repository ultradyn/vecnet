package cli

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/XertroV/vecnet/internal/platform"
)

func copyFixtureImage(dst io.Writer, src io.Reader) (int64, error) {
	if runtime.GOOS == "windows" {
		return copyFixtureImageBuffered(dst, src)
	}
	return io.Copy(dst, src)
}

func copyFixtureImageBuffered(dst io.Writer, src io.Reader) (int64, error) {
	// Windows file-to-file copies fall back to 32 KiB chunks. Hide WriteTo
	// and ReadFrom so io.CopyBuffer actually uses the larger, bounded buffer.
	return io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, make([]byte, 1<<20))
}

// The counters wrap real file I/O. Like os.File on Windows, the optional
// methods delegate back to io.Copy and would discard a supplied buffer.
type fixtureCopyReader struct {
	io.Reader
	calls, maxBuffer int
	buffer           *byte
	changedBuffer    bool
}

func (r *fixtureCopyReader) Read(p []byte) (int, error) {
	r.calls++
	r.maxBuffer = max(r.maxBuffer, cap(p))
	if r.buffer != nil && r.buffer != &p[0] {
		r.changedBuffer = true
	}
	r.buffer = &p[0]
	return r.Reader.Read(p)
}

func (r *fixtureCopyReader) WriteTo(w io.Writer) (int64, error) {
	return io.Copy(w, struct{ io.Reader }{r})
}

type fixtureCopyWriter struct {
	io.Writer
	calls int
}

func (w *fixtureCopyWriter) Write(p []byte) (int, error) {
	w.calls++
	return w.Writer.Write(p)
}

func (w *fixtureCopyWriter) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{w}, r)
}

func TestFixtureImageCopyBatchesFileIO(t *testing.T) {
	dir := t.TempDir()
	src, err := os.CreateTemp(dir, "source-")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	const size = (4 << 20) + 17 // Multiple buffers, with a non-aligned tail.
	want := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(src, want), rand.NewChaCha8([32]byte{}), size); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private-copy")
	dst, err := platform.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	r, w := &fixtureCopyReader{Reader: src}, &fixtureCopyWriter{Writer: dst}
	n, err := copyFixtureImageBuffered(w, r)
	if err != nil || n != size {
		t.Fatalf("copied %d of %d bytes: %v", n, size, err)
	}

	sourceInfo, err := src.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	if err := platform.PrivateFile(path); err != nil {
		t.Fatal(err)
	}
	copied, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	copiedInfo, err := copied.Stat()
	if err != nil {
		t.Fatal(err)
	}
	got := sha256.New()
	if n, err := io.Copy(got, copied); err != nil || n != size || !bytes.Equal(got.Sum(nil), want.Sum(nil)) || os.SameFile(sourceInfo, copiedInfo) {
		t.Fatalf("expected an independent byte-for-byte copy: size=%d hash=%x want=%x err=%v", n, got.Sum(nil), want.Sum(nil), err)
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if n, err := copyFixtureImageBuffered(dst, src); n != 0 || !errors.Is(err, os.ErrClosed) {
		t.Errorf("copy to closed destination: bytes=%d err=%v", n, err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := copyFixtureImageBuffered(dst, src); n != 0 || !errors.Is(err, os.ErrClosed) {
		t.Errorf("copy from closed source: bytes=%d err=%v", n, err)
	}

	t.Logf("copied %d bytes: reads=%d writes=%d largest read buffer=%d reused=%t", size, r.calls, w.calls, r.maxBuffer, !r.changedBuffer)
	if r.calls > 10 || w.calls > 10 || r.maxBuffer > 1<<20 || r.changedBuffer {
		t.Errorf("copy should batch file I/O with one reused buffer of at most 1 MiB")
	}
}
