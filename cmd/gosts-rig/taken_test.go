package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// exifJPEG is a JPEG start with an EXIF block saying it was taken at when
// (DateTimeOriginal, and SubSecTimeOriginal: sub).
func exifJPEG(when, sub string) []byte {
	var t bytes.Buffer
	le := binary.LittleEndian
	w16 := func(v uint16) { binary.Write(&t, le, v) }
	w32 := func(v uint32) { binary.Write(&t, le, v) }
	t.WriteString("II")
	w16(42)
	w32(8)
	// IFD0 at 8: one entry, the Exif IFD's offset (8+2+12+4 = 26).
	w16(1)
	w16(0x8769)
	w16(4)
	w32(1)
	w32(26)
	w32(0)
	// Exif IFD at 26: DateTimeOriginal (20 bytes, at 26+2+24+4 = 56), SubSec.
	w16(2)
	w16(0x9003)
	w16(2)
	w32(20)
	w32(56)
	w16(0x9291)
	w16(2)
	w32(uint32(len(sub) + 1))
	var in [4]byte
	copy(in[:], sub)
	t.Write(in[:])
	w32(0)
	t.WriteString(when + "\x00")
	seg := append([]byte("Exif\x00\x00"), t.Bytes()...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(out[4:], uint16(len(seg)+2))
	return append(append(out, seg...), 0xFF, 0xD9)
}

func TestExifTaken(t *testing.T) {
	at, ok := exifTaken(exifJPEG("2026:10:13 22:10:13", "250"))
	want := time.Date(2026, 10, 13, 22, 10, 13, 250e6, time.Local)
	if !ok || !at.Equal(want) {
		t.Fatalf("got %v %v, want %v", at, ok, want)
	}
	if _, ok := exifTaken([]byte{0xFF, 0xD8, 0xFF, 0xD9}); ok {
		t.Fatal("no EXIF: a time")
	}
}

// A capture's photos are in the order taken: DSC09999 before DSC00001 (the
// camera's numbers starting again), not by name.
func TestBatchPhotosTakenOrder(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []struct{ name, when string }{
		{"capt_DSC09998.JPG", "2026:10:13 22:10:11"},
		{"capt_DSC09999.JPG", "2026:10:13 22:10:13"},
		{"capt_DSC00001.JPG", "2026:10:13 22:10:15"},
		{"capt_DSC00002.JPG", "2026:10:13 22:10:17"},
	} {
		if err := os.WriteFile(filepath.Join(dir, p.name), exifJPEG(p.when, "0"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ps, err := batchPhotos(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.File)
	}
	want := []string{"capt_DSC09998.JPG", "capt_DSC09999.JPG", "capt_DSC00001.JPG", "capt_DSC00002.JPG"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}
