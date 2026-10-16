package transfer

import (
	"errors"
	"strings"
	"testing"
)

func TestUploadLayout(t *testing.T) {
	tests := []struct {
		name      string
		size      int64
		partSize  int64
		wantPart  int64
		wantParts int
		wantErr   string
	}{
		{"one byte", 1, 0, 16 * MiB, 1, ""},
		{"tiny file, one part", 3 * KiB, 0, 16 * MiB, 1, ""},
		{"exactly one part", 16 * MiB, 0, 16 * MiB, 1, ""},
		{"one byte over a part", 16*MiB + 1, 0, 16 * MiB, 2, ""},
		{"1 GiB in 16 MiB parts", GiB, 0, 16 * MiB, 64, ""},
		{"above 1 GiB, 64 MiB parts", GiB + 1, 0, 64 * MiB, 17, ""},
		{"100 GiB", 100 * GiB, 0, 64 * MiB, 1600, ""},
		{"625 GiB is 10,000 parts of 64 MiB", 625 * GiB, 0, 64 * MiB, 10_000, ""},
		{"above 625 GiB, bigger parts", 625*GiB + 1, 0, 65 * MiB, 9847, ""},
		{"1 TiB", TiB, 0, 105 * MiB, 9987, ""},
		{"5 TiB, the largest object", 5 * TiB, 0, 525 * MiB, 9987, ""},
		{"explicit 5 MiB parts", 50 * MiB, 5 * MiB, 5 * MiB, 10, ""},
		{"empty file", 0, 0, 0, 0, "empty"},
		{"more than 5 TiB", 5*TiB + 1, 0, 0, 0, "more than 5 TiB"},
		{"parts below 5 MiB", 50 * MiB, 4 * MiB, 0, 0, "outside the S3 limits"},
		{"parts above 5 GiB", 50 * GiB, 6 * GiB, 0, 0, "outside the S3 limits"},
		{"too many parts", 100 * GiB, 5 * MiB, 0, 0, "S3 allows 10000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, err := UploadLayout(tc.size, tc.partSize)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || l.PartSize != tc.wantPart || l.Parts != tc.wantParts {
				t.Fatalf("layout %+v, %v; want parts of %d x %d", l, err, tc.wantPart, tc.wantParts)
			}
			if l.Parts > MaxParts || (l.Parts > 1 && l.PartSize < MinPartSize) {
				t.Fatalf("layout %+v breaks the S3 limits", l)
			}
			// The parts cover the file exactly; only the last may be short.
			off, last := l.Part(l.Parts)
			if off+last != tc.size || last <= 0 || last > l.PartSize {
				t.Fatalf("last part at %d, %d bytes: does not end the file", off, last)
			}
		})
	}
}

func TestRangeLayout(t *testing.T) {
	if l := rangeLayout(0, 0); l.Parts != 0 {
		t.Fatalf("empty object: %+v", l)
	}
	l := rangeLayout(40*MiB, 0)
	if l.PartSize != DefaultRangeSize || l.Parts != 3 {
		t.Fatalf("40 MiB: %+v", l)
	}
	if off, n := l.Part(3); off != 32*MiB || n != 8*MiB {
		t.Fatalf("last range %d+%d", off, n)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := map[int64]string{
		0: "0 B", 512: "512 B", KiB: "1 KiB", 1536: "1.5 KiB", 64 * MiB: "64 MiB",
		2469606195: "2.3 GiB", 625 * GiB: "625 GiB", 5 * TiB: "5 TiB", 40*MiB + 77: "40 MiB", 1023 * KiB: "1023 KiB",
	}
	for n, want := range tests {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
