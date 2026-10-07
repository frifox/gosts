package main

import (
	"encoding/binary"
	"errors"
)

// rawPreview finds the largest JPEG embedded in a TIFF-based RAW file (Sony
// ARW, and most others: NEF, CR2, DNG): the camera stores a full-size or
// near full-size preview there, which the page can show without decoding
// the RAW. It walks every IFD (the IFD0 chain and their SubIFDs, tag 0x14a)
// for JPEGInterchangeFormat/Length pairs (tags 0x201/0x202).
func rawPreview(raw []byte) ([]byte, error) {
	if len(raw) < 8 {
		return nil, errors.New("not a TIFF file")
	}
	var bo binary.ByteOrder
	switch string(raw[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, errors.New("not a TIFF file")
	}
	if bo.Uint16(raw[2:]) != 42 {
		return nil, errors.New("not a TIFF file")
	}
	var best []byte
	seen := map[uint32]bool{}
	queue := []uint32{bo.Uint32(raw[4:])}
	for len(queue) > 0 && len(seen) < 64 {
		off := queue[0]
		queue = queue[1:]
		if off == 0 || seen[off] || int(off)+2 > len(raw) {
			continue
		}
		seen[off] = true
		n := int(bo.Uint16(raw[off:]))
		entries := int(off) + 2
		if entries+12*n+4 > len(raw) {
			continue
		}
		var jpgOff, jpgLen uint32
		for i := 0; i < n; i++ {
			e := raw[entries+12*i:]
			tag, typ, count := bo.Uint16(e), bo.Uint16(e[2:]), bo.Uint32(e[4:])
			val := bo.Uint32(e[8:])
			switch tag {
			case 0x201:
				jpgOff = val
			case 0x202:
				jpgLen = val
			case 0x14a: // SubIFDs: one offset inline, or several at val
				if typ != 4 && typ != 13 {
					continue
				}
				if count == 1 {
					queue = append(queue, val)
				} else {
					for k := uint32(0); k < count && int(val+4*k)+4 <= len(raw); k++ {
						queue = append(queue, bo.Uint32(raw[val+4*k:]))
					}
				}
			}
		}
		if jpgLen > uint32(len(best)) && uint64(jpgOff)+uint64(jpgLen) <= uint64(len(raw)) &&
			jpgLen > 2 && raw[jpgOff] == 0xFF && raw[jpgOff+1] == 0xD8 {
			best = raw[jpgOff : jpgOff+jpgLen]
		}
		queue = append(queue, bo.Uint32(raw[entries+12*n:])) // the next IFD
	}
	if best == nil {
		return nil, errors.New("no preview JPEG in the RAW file")
	}
	return best, nil
}
