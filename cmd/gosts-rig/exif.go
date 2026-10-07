package main

import (
	"bytes"
	"encoding/binary"
)

// exifFocalLength is the focal length (mm) a JPEG's EXIF says it was taken
// at (a zoom lens's, as it was then).
func exifFocalLength(jpg []byte) (float64, bool) {
	// The EXIF block: an APP1 segment, "Exif\0\0", then a TIFF structure.
	i := 2
	if len(jpg) < 4 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		return 0, false
	}
	for i+4 <= len(jpg) && jpg[i] == 0xFF {
		marker, size := jpg[i+1], int(binary.BigEndian.Uint16(jpg[i+2:]))
		if marker == 0xDA || size < 2 || i+2+size > len(jpg) { // the picture itself: no EXIF before it
			return 0, false
		}
		seg := jpg[i+4 : i+2+size]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return tiffFocalLength(seg[6:])
		}
		i += 2 + size
	}
	return 0, false
}

// tiffFocalLength finds FocalLength (0x920A, in the Exif IFD that IFD0's
// 0x8769 points to) in a TIFF structure.
func tiffFocalLength(t []byte) (float64, bool) {
	if len(t) < 8 {
		return 0, false
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0, false
	}
	// entry finds a tag in the IFD at off: its value field's offset.
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
				return e + 8, true
			}
		}
		return 0, false
	}
	v, ok := entry(bo.Uint32(t[4:]), 0x8769)
	if !ok {
		return 0, false
	}
	v, ok = entry(bo.Uint32(t[v:]), 0x920A) // a RATIONAL: its offset
	if !ok {
		return 0, false
	}
	at := int(bo.Uint32(t[v:]))
	if at+8 > len(t) {
		return 0, false
	}
	num, den := bo.Uint32(t[at:]), bo.Uint32(t[at+4:])
	if den == 0 || num == 0 {
		return 0, false
	}
	return float64(num) / float64(den), true
}
