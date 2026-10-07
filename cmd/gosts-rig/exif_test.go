package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestExifFocalLength: FocalLength read from a JPEG's EXIF (built here: a
// little-endian TIFF, IFD0 pointing to the Exif IFD, 18 mm as 180/10).
func TestExifFocalLength(t *testing.T) {
	var tf bytes.Buffer
	le := binary.LittleEndian
	w := func(v any) { binary.Write(&tf, le, v) }
	tf.WriteString("II")
	w(uint16(42))
	w(uint32(8)) // IFD0 at 8
	// IFD0: one entry, ExifIFD (0x8769, LONG) → 26
	w(uint16(1))
	w(uint16(0x8769))
	w(uint16(4))
	w(uint32(1))
	w(uint32(26))
	w(uint32(0))
	// Exif IFD at 26: one entry, FocalLength (0x920A, RATIONAL) → 44
	w(uint16(1))
	w(uint16(0x920A))
	w(uint16(5))
	w(uint32(1))
	w(uint32(44))
	w(uint32(0))
	w(uint32(180))
	w(uint32(10))
	app1 := append([]byte("Exif\x00\x00"), tf.Bytes()...)
	jpg := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	jpg = binary.BigEndian.AppendUint16(jpg, uint16(len(app1)+2))
	jpg = append(jpg, app1...)
	jpg = append(jpg, 0xFF, 0xDA, 0, 2)
	if f, ok := exifFocalLength(jpg); !ok || f != 18 {
		t.Fatalf("focal %v %v, want 18", f, ok)
	}
	if _, ok := exifFocalLength([]byte{0xFF, 0xD8, 0xFF, 0xDA, 0, 2}); ok {
		t.Error("no EXIF, yet a focal length")
	}
}
