package viewer

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// TextEncoding is an explicit on-disk encoding. Auto is resolved once from a
// bounded sample when the viewer session is opened.
type TextEncoding string

const (
	EncodingAuto        TextEncoding = "auto"
	EncodingUTF8        TextEncoding = "utf-8"
	EncodingUTF16LE     TextEncoding = "utf-16le"
	EncodingUTF16BE     TextEncoding = "utf-16be"
	EncodingWindows1251 TextEncoding = "windows-1251"
	EncodingKOI8R       TextEncoding = "koi8-r"
)

const MaxTextReadSize = MaxReadSize - 16

const ReplacementPolicy = "Некорректные последовательности заменяются символом U+FFFD."

type EncodingDetection struct {
	Encoding TextEncoding
	Source   string // bom, heuristic, or default
	BOMSize  int64
}

func ParseEncoding(value string) (TextEncoding, error) {
	result := TextEncoding(value)
	switch result {
	case EncodingAuto, EncodingUTF8, EncodingUTF16LE, EncodingUTF16BE, EncodingWindows1251, EncodingKOI8R:
		return result, nil
	default:
		return "", fmt.Errorf("viewer: unsupported encoding %q", value)
	}
}

// DetectEncoding intentionally does not try to distinguish legacy Cyrillic
// encodings: after BOM and UTF-8 checks, Auto uses Windows-1251 as a labelled
// default. KOI8-R is always available as an explicit choice.
func DetectEncoding(sample []byte) EncodingDetection {
	if bytes.HasPrefix(sample, []byte{0xef, 0xbb, 0xbf}) {
		return EncodingDetection{EncodingUTF8, "bom", 3}
	}
	if bytes.HasPrefix(sample, []byte{0xff, 0xfe}) {
		return EncodingDetection{EncodingUTF16LE, "bom", 2}
	}
	if bytes.HasPrefix(sample, []byte{0xfe, 0xff}) {
		return EncodingDetection{EncodingUTF16BE, "bom", 2}
	}
	// A sample may end in the middle of a UTF-8 rune. Only the incomplete
	// suffix is ignored; malformed complete sequences still fail validation.
	if validUTF8Sample(sample) {
		return EncodingDetection{EncodingUTF8, "heuristic", 0}
	}
	return EncodingDetection{EncodingWindows1251, "default", 0}
}

func validUTF8Sample(sample []byte) bool {
	for len(sample) > 0 {
		if !utf8.FullRune(sample) {
			return true // a bounded sample may cut only its final rune
		}
		r, size := utf8.DecodeRune(sample)
		if r == utf8.RuneError && size == 1 {
			return false
		}
		sample = sample[size:]
	}
	return true
}

func EncodingBOMSize(encoding TextEncoding, detection EncodingDetection) int64 {
	if detection.Encoding == encoding {
		return detection.BOMSize
	}
	return 0
}

// AlignStart moves an arbitrary byte address to a safe decoder boundary.
// UTF-8 needs at most three bytes of look-behind; UTF-16 is aligned relative
// to the BOM so code units never begin on an odd byte.
func AlignStart(data []byte, rawStart, requested, bomSize int64, enc TextEncoding) (int64, int) {
	start := requested
	if start < bomSize {
		start = bomSize
	}
	switch enc {
	case EncodingUTF16LE, EncodingUTF16BE:
		start = bomSize + ((start - bomSize) / 2 * 2)
		index := int(start - rawStart)
		if index >= 2 && index+1 < len(data) {
			current := textCodeUnit(data[index:index+2], enc)
			previous := textCodeUnit(data[index-2:index], enc)
			if current >= 0xdc00 && current <= 0xdfff && previous >= 0xd800 && previous <= 0xdbff {
				start -= 2
			}
		}
	case EncodingUTF8:
		index := int(start - rawStart)
		if index > len(data) {
			index = len(data)
		}
		for index > 0 && index < len(data) && data[index]&0xc0 == 0x80 {
			index--
		}
		start = rawStart + int64(index)
	}
	return start, int(start - rawStart)
}

func CompleteEnd(data []byte, startIndex, wanted int, enc TextEncoding) int {
	end := min(startIndex+wanted, len(data))
	switch enc {
	case EncodingUTF8:
		for end < len(data) && data[end]&0xc0 == 0x80 {
			end++
		}
	case EncodingUTF16LE, EncodingUTF16BE:
		if (end-startIndex)%2 != 0 && end < len(data) {
			end++
		}
		if end+1 < len(data) && end-startIndex >= 2 {
			unit := textCodeUnit(data[end-2:end], enc)
			if unit >= 0xd800 && unit <= 0xdbff {
				end += 2
			}
		}
	}
	return min(end, len(data))
}

func textCodeUnit(data []byte, enc TextEncoding) uint16 {
	if enc == EncodingUTF16BE {
		return uint16(data[1]) | uint16(data[0])<<8
	}
	return uint16(data[0]) | uint16(data[1])<<8
}

func DecodeText(data []byte, enc TextEncoding) string {
	if len(data) == 0 {
		return ""
	}
	if enc == EncodingUTF8 {
		return strings.ToValidUTF8(string(data), "\ufffd")
	}
	var codec encoding.Encoding
	switch enc {
	case EncodingUTF16LE:
		codec = unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
	case EncodingUTF16BE:
		codec = unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
	case EncodingWindows1251:
		codec = charmap.Windows1251
	case EncodingKOI8R:
		codec = charmap.KOI8R
	default:
		return strings.ToValidUTF8(string(data), "\ufffd")
	}
	decoded, _, err := transform.Bytes(codec.NewDecoder(), data)
	if err != nil {
		// x/text decoders normally substitute malformed input. Preserve that
		// predictable, non-fatal contract even if a decoder reports an error.
		return strings.ToValidUTF8(string(decoded), "\ufffd") + "\ufffd"
	}
	return string(decoded)
}
