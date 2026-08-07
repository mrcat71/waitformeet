// Package media takes uploaded pictures and puts them safely on the volume.
//
// "Safely" means three things. Every file is decoded and re-encoded rather than
// stored as received, which strips EXIF and therefore the GPS coordinates most
// phones write into a photo. Only real images get through, decided by decoding
// them rather than by trusting a filename or a Content-Type header. And names on
// disk are generated, never taken from the client.
package media

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"

	// Registering the decoders is what lets image.Decode recognise a format. WebP
	// is read-only in x/image, which is fine: everything is re-encoded as JPEG.
	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"golang.org/x/image/draw"
)

const (
	// ThumbMaxEdge is the longest side of a generated thumbnail.
	ThumbMaxEdge = 480
	// OriginalMaxEdge caps the stored picture. Phone cameras produce far more
	// pixels than a page ever shows, and the volume is not free.
	OriginalMaxEdge = 2560

	jpegQuality      = 86
	thumbJPEGQuality = 78

	// decodeLimitPixels rejects images whose declared dimensions would allocate an
	// enormous buffer. A small file can claim to be 60000x60000.
	decodeLimitPixels = 50 << 20
)

var (
	// ErrNotAnImage reports a file that does not decode as a supported picture.
	ErrNotAnImage = errors.New("media: that file is not a JPEG, PNG, GIF or WebP image")
	// ErrTooLarge reports an image whose pixel count is unreasonable.
	ErrTooLarge = errors.New("media: that image has too many pixels")
)

// Stored describes a picture that has been written to the volume.
type Stored struct {
	Filename      string
	ThumbFilename string
	Width         int
	Height        int
}

// Store writes pictures under a data directory.
type Store struct {
	originalDir string
	thumbDir    string
}

// NewStore prepares the directories under dataDir/media.
func NewStore(dataDir string) (*Store, error) {
	s := &Store{
		originalDir: filepath.Join(dataDir, "media", "original"),
		thumbDir:    filepath.Join(dataDir, "media", "thumb"),
	}
	for _, dir := range []string{s.originalDir, s.thumbDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("media: prepare %s: %w", dir, err)
		}
	}
	return s, nil
}

// OriginalPath returns where a stored picture lives.
func (s *Store) OriginalPath(name string) string { return filepath.Join(s.originalDir, name) }

// ThumbPath returns where a generated thumbnail lives.
func (s *Store) ThumbPath(name string) string { return filepath.Join(s.thumbDir, name) }

// Save decodes an upload, re-encodes it without metadata, and writes both the
// picture and its thumbnail.
func (s *Store) Save(r io.Reader) (*Stored, error) {
	// image.Decode picks the format by sniffing content, so a .jpg that is really
	// a script simply fails to decode. The registered decoders are the only formats
	// that can get through.
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAnImage, err)
	}

	bounds := src.Bounds()
	if bounds.Dx()*bounds.Dy() > decodeLimitPixels {
		return nil, ErrTooLarge
	}

	name, err := randomName()
	if err != nil {
		return nil, err
	}

	original := resizeToFit(src, OriginalMaxEdge)
	if err := writeJPEG(s.OriginalPath(name), original, jpegQuality); err != nil {
		return nil, err
	}

	thumb := resizeToFit(src, ThumbMaxEdge)
	if err := writeJPEG(s.ThumbPath(name), thumb, thumbJPEGQuality); err != nil {
		// Leaving an orphan original behind would show a broken gallery entry.
		if rmErr := os.Remove(s.OriginalPath(name)); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("media: clean up %s: %w", name, rmErr))
		}
		return nil, err
	}

	size := original.Bounds()
	return &Stored{
		Filename:      name,
		ThumbFilename: name,
		Width:         size.Dx(),
		Height:        size.Dy(),
	}, nil
}

// Remove deletes a picture and its thumbnail. A missing file is not an error: the
// database row is the thing that matters, and it is already gone by this point.
func (s *Store) Remove(name string) error {
	var errs []error
	for _, path := range []string{s.OriginalPath(name), s.ThumbPath(name)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("media: remove %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// resizeToFit scales an image down so its longest side is at most maxEdge.
// Images already small enough are returned untouched.
func resizeToFit(src image.Image, maxEdge int) image.Image {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= maxEdge && h <= maxEdge {
		return src
	}

	scale := float64(maxEdge) / float64(max(w, h))
	dstW := max(1, int(float64(w)*scale))
	dstH := max(1, int(float64(h)*scale))

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	// CatmullRom is slower than the alternatives and noticeably sharper, which is
	// what a photo of someone you miss deserves.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)
	return dst
}

// writeJPEG encodes to a temporary file and renames it into place, so a crash
// mid-write cannot leave a half-written picture that the gallery then serves.
func writeJPEG(path string, img image.Image, quality int) error {
	tmp := path + ".tmp"

	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("media: create %s: %w", tmp, err)
	}

	if err := jpeg.Encode(file, img, &jpeg.Options{Quality: quality}); err != nil {
		file.Close()
		os.Remove(tmp)
		return fmt.Errorf("media: encode %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("media: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("media: install %s: %w", path, err)
	}
	return nil
}

// randomName generates the on-disk filename. It is random rather than derived from
// the upload, so nothing a visitor controls ever reaches the filesystem, and one
// picture's name cannot be guessed from another's.
func randomName() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("media: generate a filename: %w", err)
	}
	return hex.EncodeToString(buf[:]) + ".jpg", nil
}
