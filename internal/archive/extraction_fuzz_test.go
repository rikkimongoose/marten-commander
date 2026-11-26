package archive

import (
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzValidateExtractionPath(f *testing.F) {
	for _, seed := range []string{
		"file.txt", "Каталог/файл с пробелами.txt", "../escape", "/absolute", `C:\escape`,
		`\\server\share`, "a/../../b", "a//b", "a/./b", "name:stream", "bad\x00name",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		validated, err := ValidateExtractionPath(name)
		if err != nil {
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("unexpected error for %q: %v", name, err)
			}
			return
		}
		if validated == "" || !utf8.ValidString(validated) || strings.HasPrefix(validated, "/") || strings.ContainsAny(validated, "\\:\x00") {
			t.Fatalf("unsafe accepted path %q from %q", validated, name)
		}
		for _, component := range strings.Split(validated, "/") {
			if component == "" || component == "." || component == ".." {
				t.Fatalf("unsafe component %q in %q", component, validated)
			}
		}
		if second, secondErr := ValidateExtractionPath(validated); secondErr != nil || second != validated {
			t.Fatalf("validation is not idempotent: %q -> %q, %v", validated, second, secondErr)
		}
	})
}

func FuzzExtractionCounters(f *testing.F) {
	f.Add(uint64(0), uint64(0), uint64(1))
	f.Add(uint64(math.MaxUint64-1), uint64(2), uint64(math.MaxUint64))
	f.Add(uint64(10), uint64(20), uint64(25))
	f.Fuzz(func(t *testing.T, current, addition, maximum uint64) {
		result, err := addChecked(current, addition, maximum)
		if err != nil {
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		if result < current || result < addition || result > maximum || result != current+addition {
			t.Fatalf("invalid sum: %d + %d = %d with max %d", current, addition, result, maximum)
		}
	})
}

func FuzzExpansionRatio(f *testing.F) {
	f.Add(uint64(0), int64(0), uint64(1))
	f.Add(uint64(1001), int64(1), uint64(1000))
	f.Add(uint64(math.MaxUint64), int64(math.MaxInt64), uint64(math.MaxUint64))
	f.Fuzz(func(t *testing.T, expanded uint64, compressed int64, maximum uint64) {
		err := checkRatio(expanded, &compressed, maximum)
		if maximum == 0 || compressed < 0 {
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("invalid input accepted: expanded=%d compressed=%d max=%d err=%v", expanded, compressed, maximum, err)
			}
			return
		}
		if compressed == 0 && expanded > 0 && !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("zero compressed size accepted expanded data")
		}
		if compressed > 0 && uint64(compressed) <= math.MaxUint64/maximum {
			exceeded := expanded > uint64(compressed)*maximum
			if exceeded != errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("ratio mismatch: expanded=%d compressed=%d max=%d err=%v", expanded, compressed, maximum, err)
			}
		}
	})
}
