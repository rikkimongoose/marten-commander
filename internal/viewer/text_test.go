package viewer

import (
	"bytes"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

func encoded(t *testing.T, codec encoding.Encoding, value string) []byte {
	t.Helper()
	result, _, err := transform.Bytes(codec.NewEncoder(), []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDetectEncodingHasLimitedDocumentedSemantics(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		enc    TextEncoding
		source string
		bom    int64
	}{
		{"empty UTF-8", nil, EncodingUTF8, "heuristic", 0},
		{"UTF-8 ASCII", []byte("plain"), EncodingUTF8, "heuristic", 0},
		{"UTF-8 Cyrillic", []byte("Привет"), EncodingUTF8, "heuristic", 0},
		{"UTF-8 BOM", append([]byte{0xef, 0xbb, 0xbf}, []byte("text")...), EncodingUTF8, "bom", 3},
		{"UTF-16 LE", []byte{0xff, 0xfe, 'A', 0}, EncodingUTF16LE, "bom", 2},
		{"UTF-16 BE", []byte{0xfe, 0xff, 0, 'A'}, EncodingUTF16BE, "bom", 2},
		{"legacy is not guessed", []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}, EncodingWindows1251, "default", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := DetectEncoding(test.data)
			if got.Encoding != test.enc || got.Source != test.source || got.BOMSize != test.bom {
				t.Fatalf("detection = %#v", got)
			}
		})
	}
}

func TestDecodeEverySupportedTextEncoding(t *testing.T) {
	want := "Привет, 😀!"
	utf16LE := encoded(t, unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), want)
	utf16BE := encoded(t, unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM), want)
	if got := DecodeText([]byte(want), EncodingUTF8); got != want {
		t.Fatalf("UTF-8 = %q", got)
	}
	if got := DecodeText(utf16LE, EncodingUTF16LE); got != want {
		t.Fatalf("UTF-16LE = %q", got)
	}
	if got := DecodeText(utf16BE, EncodingUTF16BE); got != want {
		t.Fatalf("UTF-16BE = %q", got)
	}
	legacyWant := "Привет"
	if got := DecodeText(encoded(t, charmap.Windows1251, legacyWant), EncodingWindows1251); got != legacyWant {
		t.Fatalf("Windows-1251 = %q", got)
	}
	if got := DecodeText(encoded(t, charmap.KOI8R, legacyWant), EncodingKOI8R); got != legacyWant {
		t.Fatalf("KOI8-R = %q", got)
	}
	if !utf8.ValidString(DecodeText([]byte{0xff, 'A'}, EncodingUTF8)) || DecodeText([]byte{0xff}, EncodingUTF8) != "�" {
		t.Fatal("invalid UTF-8 was not replaced")
	}
}

func TestUTFBoundariesNeverSplitCharacters(t *testing.T) {
	utf8Data := []byte("abcЖdef")
	start, index := AlignStart(utf8Data, 0, 4, 0, EncodingUTF8)
	if start != 3 || index != 3 {
		t.Fatalf("UTF-8 start = %d/%d", start, index)
	}
	end := CompleteEnd(utf8Data, 0, 4, EncodingUTF8)
	if got := DecodeText(utf8Data[:end], EncodingUTF8); got != "abcЖ" {
		t.Fatalf("UTF-8 boundary = %q", got)
	}

	value := "A😀B"
	data := encoded(t, unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), value)
	_, index = AlignStart(data, 0, 3, 0, EncodingUTF16LE)
	if index != 2 {
		t.Fatalf("UTF-16 alignment = %d", index)
	}
	start, index = AlignStart(data, 0, 4, 0, EncodingUTF16LE)
	if start != 2 || index != 2 {
		t.Fatalf("UTF-16 low surrogate start = %d/%d", start, index)
	}
	end = CompleteEnd(data, 0, 4, EncodingUTF16LE)
	if got := DecodeText(data[:end], EncodingUTF16LE); got != "A😀" {
		t.Fatalf("UTF-16 surrogate boundary = %q, units=%v", got, utf16.Encode([]rune(value)))
	}
}

func TestTextPreservesLineEndingsAndLongLines(t *testing.T) {
	for _, value := range []string{"one\ntwo\n", "one\r\ntwo", "no trailing newline", "", stringsOf('x', MaxTextReadSize)} {
		if got := DecodeText([]byte(value), EncodingUTF8); got != value {
			t.Fatalf("round trip changed content of length %d", len(value))
		}
	}
}

func stringsOf(value byte, count int) string { return string(bytes.Repeat([]byte{value}, count)) }
