package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
)

// samplePNG builds an image of the given size, with a recognisable colour so a
// resize can be checked for having produced something rather than a blank canvas.
func samplePNG(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 120, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return buf.Bytes()
}

// jpegWithEXIF builds a JPEG carrying an EXIF segment holding GPS-looking bytes.
//
// This is the case the package exists for: phones write coordinates into photos,
// and a site that stores uploads verbatim publishes the address they were taken at.
func jpegWithEXIF(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg.Encode() error = %v", err)
	}
	encoded := buf.Bytes()

	// An APP1/Exif segment goes straight after the two-byte SOI marker.
	payload := []byte("Exif\x00\x00SECRET-GPS-COORDINATES")
	segment := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte((len(payload) + 2) & 0xFF)}
	segment = append(segment, payload...)

	withEXIF := make([]byte, 0, len(encoded)+len(segment))
	withEXIF = append(withEXIF, encoded[:2]...)
	withEXIF = append(withEXIF, segment...)
	withEXIF = append(withEXIF, encoded[2:]...)
	return withEXIF
}

func newTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return s
}

// The whole point of re-encoding: nothing the camera wrote survives.
func TestSaveStripsEXIF(t *testing.T) {
	s := newTestStore(t)

	source := jpegWithEXIF(t)
	if !bytes.Contains(source, []byte("SECRET-GPS-COORDINATES")) {
		t.Fatal("the test fixture does not actually carry EXIF")
	}

	stored, err := s.Save(bytes.NewReader(source))
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	for _, path := range []string{s.OriginalPath(stored.Filename), s.ThumbPath(stored.ThumbFilename)} {
		written, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		if bytes.Contains(written, []byte("SECRET-GPS-COORDINATES")) {
			t.Errorf("%s still carries the EXIF payload", path)
		}
		if bytes.Contains(written, []byte("Exif")) {
			t.Errorf("%s still carries an Exif marker", path)
		}
	}
}

func TestSaveRejectsNonImages(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty", body: nil},
		{name: "plain text", body: []byte("this is not a picture")},
		{name: "a script pretending to be a jpeg", body: []byte("<?php system($_GET['c']); ?>")},
		{name: "an svg, which is markup and stays out", body: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`)},
		{name: "truncated png", body: samplePNG(t, 8, 8)[:20]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)

			_, err := s.Save(bytes.NewReader(tt.body))
			if !errors.Is(err, ErrNotAnImage) {
				t.Errorf("Save() error = %v, want ErrNotAnImage", err)
			}
		})
	}
}

func TestSaveResizes(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		height     int
		wantWidth  int
		wantHeight int
	}{
		{name: "small images are left alone", width: 100, height: 80, wantWidth: 100, wantHeight: 80},
		{
			name: "a wide image is capped on its long edge",
			// 4000x2000 scales to the 2560 cap on the width.
			width: 4000, height: 2000, wantWidth: OriginalMaxEdge, wantHeight: OriginalMaxEdge / 2,
		},
		{
			name:  "a tall image is capped on its height",
			width: 1500, height: 3000, wantWidth: OriginalMaxEdge / 2, wantHeight: OriginalMaxEdge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)

			stored, err := s.Save(bytes.NewReader(samplePNG(t, tt.width, tt.height)))
			if err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			if stored.Width != tt.wantWidth || stored.Height != tt.wantHeight {
				t.Errorf("stored size = %dx%d, want %dx%d",
					stored.Width, stored.Height, tt.wantWidth, tt.wantHeight)
			}

			thumb, err := os.Open(s.ThumbPath(stored.ThumbFilename))
			if err != nil {
				t.Fatalf("open the thumbnail: %v", err)
			}
			defer thumb.Close()

			cfg, _, err := image.DecodeConfig(thumb)
			if err != nil {
				t.Fatalf("decode the thumbnail: %v", err)
			}
			if cfg.Width > ThumbMaxEdge || cfg.Height > ThumbMaxEdge {
				t.Errorf("thumbnail is %dx%d, want neither side above %d",
					cfg.Width, cfg.Height, ThumbMaxEdge)
			}
		})
	}
}

// Filenames must come from the server, never from the upload, and never repeat.
func TestSaveGeneratesItsOwnNames(t *testing.T) {
	s := newTestStore(t)

	seen := make(map[string]bool)
	for range 5 {
		stored, err := s.Save(bytes.NewReader(samplePNG(t, 32, 32)))
		if err != nil {
			t.Fatalf("Save() error = %v", err)
		}
		if seen[stored.Filename] {
			t.Fatalf("filename %q was generated twice", stored.Filename)
		}
		seen[stored.Filename] = true

		if strings.ContainsAny(stored.Filename, `/\.`) && !strings.HasSuffix(stored.Filename, ".jpg") {
			t.Errorf("filename %q contains a path separator", stored.Filename)
		}
		if !strings.HasSuffix(stored.Filename, ".jpg") {
			t.Errorf("filename %q does not end in .jpg; everything is re-encoded", stored.Filename)
		}
	}
}

// A PNG or GIF upload is converted, so the gallery only ever serves one format.
func TestSaveNormalisesFormat(t *testing.T) {
	s := newTestStore(t)

	stored, err := s.Save(bytes.NewReader(samplePNG(t, 40, 40)))
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	file, err := os.Open(s.OriginalPath(stored.Filename))
	if err != nil {
		t.Fatalf("open the stored picture: %v", err)
	}
	defer file.Close()

	_, format, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("decode the stored picture: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("stored format = %q, want jpeg", format)
	}
}

func TestRemove(t *testing.T) {
	s := newTestStore(t)

	stored, err := s.Save(bytes.NewReader(samplePNG(t, 32, 32)))
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := s.Remove(stored.Filename); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	for _, path := range []string{s.OriginalPath(stored.Filename), s.ThumbPath(stored.ThumbFilename)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists after Remove()", path)
		}
	}

	// Removing again must not complain: the row is already gone, so the caller has
	// nothing left to do about a missing file.
	if err := s.Remove(stored.Filename); err != nil {
		t.Errorf("second Remove() error = %v, want nil", err)
	}
}
