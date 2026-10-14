package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// When a photo was taken, to put a capture's photos in order: the camera's
// file names aren't (after DSC09999 a Sony starts again at DSC00001). Its
// EXIF's DateTimeOriginal with SubSecTimeOriginal (the camera's clock,
// to the millisecond on a Sony), from a JPEG or a TIFF-based RAW (.ARW);
// else, the file's time (gosts-rig saved it as it came over: in order too).

// photoTaken is when the photo at path was taken (see above).
func photoTaken(path string, mod time.Time, size int64) time.Time {
	takenCache.Lock()
	if c, ok := takenCache.m[path]; ok && c.mod.Equal(mod) && c.size == size {
		takenCache.Unlock()
		return c.at
	}
	takenCache.Unlock()
	at := mod
	if f, err := os.Open(path); err == nil {
		head := make([]byte, 128<<10) // (the EXIF's at the start)
		n, _ := io.ReadFull(f, head)
		f.Close()
		if t, ok := exifTaken(head[:n]); ok {
			at = t
		}
	}
	takenCache.Lock()
	if takenCache.m == nil {
		takenCache.m = map[string]takenEntry{}
	}
	takenCache.m[path] = takenEntry{mod, size, at}
	takenCache.Unlock()
	return at
}

type takenEntry struct {
	mod  time.Time
	size int64
	at   time.Time
}

var takenCache struct {
	sync.Mutex
	m map[string]takenEntry
}

// exifTaken reads DateTimeOriginal (+ SubSecTimeOriginal) from the start of
// a JPEG (its EXIF segment) or of a TIFF-based file (a RAW).
func exifTaken(b []byte) (time.Time, bool) {
	if len(b) >= 4 && b[0] == 0xFF && b[1] == 0xD8 {
		for i := 2; i+4 <= len(b) && b[i] == 0xFF; {
			marker, size := b[i+1], int(binary.BigEndian.Uint16(b[i+2:]))
			if marker == 0xDA || size < 2 || i+2+size > len(b) {
				return time.Time{}, false
			}
			seg := b[i+4 : i+2+size]
			if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
				return tiffTaken(seg[6:])
			}
			i += 2 + size
		}
		return time.Time{}, false
	}
	return tiffTaken(b)
}

// tiffTaken finds DateTimeOriginal (0x9003) and SubSecTimeOriginal (0x9291)
// in a TIFF structure's Exif IFD (IFD0's 0x8769).
func tiffTaken(t []byte) (time.Time, bool) {
	if len(t) < 8 {
		return time.Time{}, false
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return time.Time{}, false
	}
	// entry is a tag's entry in the IFD at off (its offset), if it's there.
	entry := func(off uint32, tag uint16) (int, bool) {
		if int(off)+2 > len(t) {
			return 0, false
		}
		n := int(bo.Uint16(t[off:]))
		for k := 0; k < n; k++ {
			e := int(off) + 2 + 12*k
			if e+12 > len(t) {
				return 0, false
			}
			if bo.Uint16(t[e:]) == tag {
				return e, true
			}
		}
		return 0, false
	}
	// ascii is an ASCII entry's text (in the entry if 4 bytes or fewer).
	ascii := func(e int) string {
		n := int(bo.Uint32(t[e+4:]))
		at := e + 8
		if n > 4 {
			at = int(bo.Uint32(t[e+8:]))
		}
		if at < 0 || at+n > len(t) {
			return ""
		}
		return strings.TrimRight(string(t[at:at+n]), "\x00 ")
	}
	e, ok := entry(bo.Uint32(t[4:]), 0x8769)
	if !ok {
		return time.Time{}, false
	}
	exif := bo.Uint32(t[e+8:])
	e, ok = entry(exif, 0x9003)
	if !ok {
		return time.Time{}, false
	}
	at, err := time.ParseInLocation("2006:01:02 15:04:05", ascii(e), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	if e, ok := entry(exif, 0x9291); ok {
		if frac, err := strconv.ParseFloat("0."+ascii(e), 64); err == nil { // ("123": 0.123 s)
			at = at.Add(time.Duration(frac * float64(time.Second)))
		}
	}
	return at, true
}
