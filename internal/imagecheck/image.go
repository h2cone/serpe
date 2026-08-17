// Package imagecheck validates the bounded container structure of static
// inline images without fully decoding their pixel payload.
package imagecheck

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

var (
	// ErrInvalid reports a malformed, animated, or MIME-mismatched container.
	ErrInvalid = errors.New("invalid image")
	// ErrLimit reports a byte, dimension, pixel, or parser-work limit.
	ErrLimit = errors.New("image limit exceeded")
)

const (
	MIMEPNG  = "image/png"
	MIMEJPEG = "image/jpeg"
	MIMEGIF  = "image/gif"
	MIMEWebP = "image/webp"
)

// Limits bounds inspection work and declared image dimensions.
type Limits struct {
	MaxBytes   int64
	MaxWidth   int
	MaxHeight  int
	MaxPixels  int64
	MaxRecords int
}

// DefaultLimits is the shared inspect ceiling used by tools, projection, and
// tool-result downgrade.
func DefaultLimits() Limits {
	return Limits{MaxBytes: 7 << 20, MaxWidth: 8192, MaxHeight: 8192, MaxPixels: 40_000_000}
}

// Info is the validated container metadata.
type Info struct {
	MIMEType string
	Width    int
	Height   int
}

// Detect returns the MIME indicated by a supported magic prefix.
func Detect(data []byte) (string, bool) {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return MIMEPNG, true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return MIMEJPEG, true
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return MIMEGIF, true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return MIMEWebP, true
	default:
		return "", false
	}
}

// Inspect validates a supported static container and its declared canvas.
// It deliberately does not decompress PNG IDAT, JPEG entropy, GIF LZW, or
// WebP bitstreams.
func Inspect(mime string, data []byte, limits Limits) (Info, error) {
	if len(data) == 0 {
		return Info{}, invalid("image is empty")
	}
	if limits.MaxBytes > 0 && int64(len(data)) > limits.MaxBytes {
		return Info{}, limit("image bytes exceed limit")
	}
	if limits.MaxRecords <= 0 {
		limits.MaxRecords = 65_536
	}
	var (
		width  int
		height int
		err    error
	)
	switch mime {
	case MIMEPNG:
		width, height, err = inspectPNG(data, limits.MaxRecords)
	case MIMEJPEG:
		width, height, err = inspectJPEG(data, limits.MaxRecords)
	case MIMEGIF:
		width, height, err = inspectGIF(data, limits.MaxRecords)
	case MIMEWebP:
		width, height, err = inspectWebP(data, limits.MaxRecords)
	default:
		return Info{}, invalid("unsupported image MIME %q", mime)
	}
	if err != nil {
		return Info{}, err
	}
	if detected, ok := Detect(data); !ok || detected != mime {
		return Info{}, invalid("declared MIME does not match image container")
	}
	if width <= 0 || height <= 0 {
		return Info{}, invalid("image dimensions must be positive")
	}
	if limits.MaxWidth > 0 && width > limits.MaxWidth {
		return Info{}, limit("image width exceeds limit")
	}
	if limits.MaxHeight > 0 && height > limits.MaxHeight {
		return Info{}, limit("image height exceeds limit")
	}
	if width > int(^uint(0)>>1)/height {
		return Info{}, limit("image pixel count overflows")
	}
	pixels := int64(width) * int64(height)
	if limits.MaxPixels > 0 && pixels > limits.MaxPixels {
		return Info{}, limit("image pixel count exceeds limit")
	}
	return Info{MIMEType: mime, Width: width, Height: height}, nil
}

func inspectPNG(data []byte, maxRecords int) (int, int, error) {
	if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		return 0, 0, invalid("image is not PNG")
	}
	pos := 8
	records := 0
	width, height := 0, 0
	sawIDAT := false
	sawIEND := false
	for pos < len(data) {
		if len(data)-pos < 12 {
			return 0, 0, invalid("truncated PNG chunk")
		}
		records++
		if records > maxRecords {
			return 0, 0, limit("PNG record budget exceeded")
		}
		n := uint64(binary.BigEndian.Uint32(data[pos : pos+4]))
		if n > uint64(len(data)-pos-12) {
			return 0, 0, invalid("truncated PNG chunk")
		}
		typ := data[pos+4 : pos+8]
		bodyEnd := pos + 8 + int(n)
		body := data[pos+8 : bodyEnd]
		wantCRC := binary.BigEndian.Uint32(data[bodyEnd : bodyEnd+4])
		crc := crc32.NewIEEE()
		_, _ = crc.Write(typ)
		_, _ = crc.Write(body)
		if crc.Sum32() != wantCRC {
			return 0, 0, invalid("PNG chunk checksum mismatch")
		}
		name := string(typ)
		switch name {
		case "IHDR":
			if records != 1 || len(body) != 13 || width != 0 {
				return 0, 0, invalid("invalid PNG IHDR")
			}
			w := binary.BigEndian.Uint32(body[:4])
			h := binary.BigEndian.Uint32(body[4:8])
			if uint64(w) > uint64(^uint(0)>>1) || uint64(h) > uint64(^uint(0)>>1) {
				return 0, 0, limit("PNG canvas is too large")
			}
			width, height = int(w), int(h)
		case "IDAT":
			if width == 0 || sawIEND {
				return 0, 0, invalid("PNG IDAT is out of order")
			}
			sawIDAT = true
		case "acTL", "fcTL", "fdAT":
			return 0, 0, invalid("animated PNG is not allowed")
		case "IEND":
			if len(body) != 0 || !sawIDAT {
				return 0, 0, invalid("invalid PNG IEND")
			}
			sawIEND = true
			pos = bodyEnd + 4
			if pos != len(data) {
				return 0, 0, invalid("PNG has trailing data")
			}
			return width, height, nil
		}
		pos = bodyEnd + 4
	}
	if !sawIEND {
		return 0, 0, invalid("PNG is missing IEND")
	}
	return width, height, nil
}

func inspectJPEG(data []byte, maxRecords int) (int, int, error) {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 0, 0, invalid("image is not JPEG")
	}
	pos := 2
	records := 0
	width, height := 0, 0
	inScan := false
	sawScan := false
	for pos < len(data) {
		if inScan && data[pos] != 0xff {
			pos++
			continue
		}
		if data[pos] != 0xff {
			return 0, 0, invalid("invalid JPEG marker")
		}
		for pos < len(data) && data[pos] == 0xff {
			pos++
		}
		if pos >= len(data) {
			return 0, 0, invalid("truncated JPEG marker")
		}
		marker := data[pos]
		pos++
		if inScan && marker == 0x00 {
			continue
		}
		if inScan && marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		inScan = false
		records++
		if records > maxRecords {
			return 0, 0, limit("JPEG record budget exceeded")
		}
		switch marker {
		case 0xd9:
			if width == 0 || !sawScan || pos != len(data) {
				return 0, 0, invalid("invalid JPEG end marker")
			}
			return width, height, nil
		case 0xd8, 0x01:
			continue
		}
		if marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if pos+2 > len(data) {
			return 0, 0, invalid("truncated JPEG segment")
		}
		n := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		if n < 2 || n > len(data)-pos {
			return 0, 0, invalid("truncated JPEG segment")
		}
		segment := data[pos+2 : pos+n]
		pos += n
		if isSOF(marker) {
			if len(segment) < 6 {
				return 0, 0, invalid("invalid JPEG SOF")
			}
			height = int(binary.BigEndian.Uint16(segment[1:3]))
			width = int(binary.BigEndian.Uint16(segment[3:5]))
		}
		if marker == 0xda {
			sawScan = true
			inScan = true
		}
	}
	return 0, 0, invalid("JPEG is missing EOI")
}

func isSOF(marker byte) bool {
	return (marker >= 0xc0 && marker <= 0xc3) || (marker >= 0xc5 && marker <= 0xc7) ||
		(marker >= 0xc9 && marker <= 0xcb) || (marker >= 0xcd && marker <= 0xcf)
}

func inspectGIF(data []byte, maxRecords int) (int, int, error) {
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return 0, 0, invalid("image is not GIF")
	}
	width := int(binary.LittleEndian.Uint16(data[6:8]))
	height := int(binary.LittleEndian.Uint16(data[8:10]))
	pos := 13
	if data[10]&0x80 != 0 {
		n := 3 * (1 << (1 + int(data[10]&7)))
		if n > len(data)-pos {
			return 0, 0, invalid("truncated GIF color table")
		}
		pos += n
	}
	frames := 0
	records := 0
	for pos < len(data) {
		records++
		if records > maxRecords {
			return 0, 0, limit("GIF record budget exceeded")
		}
		switch data[pos] {
		case 0x2c:
			frames++
			if frames > 1 {
				return 0, 0, invalid("animated GIF is not allowed")
			}
			if len(data)-pos < 10 {
				return 0, 0, invalid("truncated GIF image descriptor")
			}
			packed := data[pos+9]
			pos += 10
			if packed&0x80 != 0 {
				n := 3 * (1 << (1 + int(packed&7)))
				if n > len(data)-pos {
					return 0, 0, invalid("truncated GIF local color table")
				}
				pos += n
			}
			if pos >= len(data) {
				return 0, 0, invalid("truncated GIF image data")
			}
			pos++ // LZW minimum code size
			if err := skipGIFBlocks(data, &pos, &records, maxRecords); err != nil {
				return 0, 0, err
			}
		case 0x21:
			if len(data)-pos < 2 {
				return 0, 0, invalid("truncated GIF extension")
			}
			pos += 2
			if err := skipGIFBlocks(data, &pos, &records, maxRecords); err != nil {
				return 0, 0, err
			}
		case 0x3b:
			pos++
			if frames != 1 || pos != len(data) {
				return 0, 0, invalid("GIF must contain exactly one frame and no trailing data")
			}
			return width, height, nil
		default:
			return 0, 0, invalid("invalid GIF block")
		}
	}
	return 0, 0, invalid("GIF is missing trailer")
}

func skipGIFBlocks(data []byte, pos, records *int, maxRecords int) error {
	for *pos < len(data) {
		*records++
		if *records > maxRecords {
			return limit("GIF record budget exceeded")
		}
		n := int(data[*pos])
		*pos++
		if n == 0 {
			return nil
		}
		if n > len(data)-*pos {
			return invalid("truncated GIF data block")
		}
		*pos += n
	}
	return invalid("truncated GIF data blocks")
}

func inspectWebP(data []byte, maxRecords int) (int, int, error) {
	if len(data) < 16 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, invalid("image is not WebP")
	}
	if uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return 0, 0, invalid("WebP RIFF length mismatch")
	}
	pos := 12
	records := 0
	width, height := 0, 0
	sawImage := false
	for pos < len(data) {
		if len(data)-pos < 8 {
			return 0, 0, invalid("truncated WebP chunk")
		}
		records++
		if records > maxRecords {
			return 0, 0, limit("WebP record budget exceeded")
		}
		kind := string(data[pos : pos+4])
		n := uint64(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if n > uint64(len(data)-pos) {
			return 0, 0, invalid("truncated WebP chunk")
		}
		body := data[pos : pos+int(n)]
		pos += int(n)
		if n&1 != 0 {
			if pos >= len(data) {
				return 0, 0, invalid("WebP chunk is missing padding")
			}
			pos++
		}
		switch kind {
		case "VP8X":
			if len(body) != 10 || body[0]&0x02 != 0 {
				return 0, 0, invalid("invalid or animated VP8X")
			}
			width = 1 + int(body[4]) + int(body[5])<<8 + int(body[6])<<16
			height = 1 + int(body[7]) + int(body[8])<<8 + int(body[9])<<16
		case "ANIM", "ANMF":
			return 0, 0, invalid("animated WebP is not allowed")
		case "VP8 ":
			w, h, err := vp8Size(body)
			if err != nil {
				return 0, 0, err
			}
			if width == 0 {
				width, height = w, h
			}
			sawImage = true
		case "VP8L":
			w, h, err := vp8lSize(body)
			if err != nil {
				return 0, 0, err
			}
			if width == 0 {
				width, height = w, h
			}
			sawImage = true
		}
	}
	if !sawImage || width == 0 {
		return 0, 0, invalid("WebP is missing an image bitstream")
	}
	return width, height, nil
}

func vp8Size(body []byte) (int, int, error) {
	if len(body) < 10 || body[3] != 0x9d || body[4] != 0x01 || body[5] != 0x2a {
		return 0, 0, invalid("invalid VP8 start code")
	}
	width := int(binary.LittleEndian.Uint16(body[6:8])) & 0x3fff
	height := int(binary.LittleEndian.Uint16(body[8:10])) & 0x3fff
	return width, height, nil
}

func vp8lSize(body []byte) (int, int, error) {
	if len(body) < 5 || body[0] != 0x2f {
		return 0, 0, invalid("invalid VP8L header")
	}
	bits := uint32(body[1]) | uint32(body[2])<<8 | uint32(body[3])<<16 | uint32(body[4])<<24
	return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func limit(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrLimit, fmt.Sprintf(format, args...))
}
