package transfer

import (
	"fmt"
	"strconv"
)

// Sizes, and the S3 multipart limits.
const (
	KiB = 1 << 10
	MiB = 1 << 20
	GiB = 1 << 30
	TiB = 1 << 40

	// MinPartSize is the smallest part S3 accepts, except the last one.
	MinPartSize = 5 * MiB
	// MaxPartSize is the largest part S3 accepts.
	MaxPartSize = 5 * GiB
	// MaxParts is the most parts of one multipart upload.
	MaxParts = 10_000
	// MaxObjectSize is the largest S3 object.
	MaxObjectSize = 5 * TiB

	// DefaultRangeSize is the size of the ranged GETs of a download.
	DefaultRangeSize = 16 * MiB
)

// Layout is how a file is split into parts (upload) or ranges (download).
type Layout struct {
	Size     int64
	PartSize int64
	Parts    int
}

// Part returns the offset and the length of part n, counted from 1.
func (l Layout) Part(n int) (offset, length int64) {
	offset = int64(n-1) * l.PartSize
	return offset, min(l.PartSize, l.Size-offset)
}

// UploadLayout splits size bytes into the parts of a multipart upload. With
// partSize 0 the part size follows the file: 16 MiB up to 1 GiB (64 parts at
// most), 64 MiB up to 625 GiB (10,000 parts), then the smallest whole MiB
// that keeps the parts within 10,000.
func UploadLayout(size, partSize int64) (Layout, error) {
	switch {
	case size <= 0:
		return Layout{}, invalidf("the file is empty")
	case size > MaxObjectSize:
		return Layout{}, invalidf("%s is more than 5 TiB, the largest S3 object", FormatBytes(size))
	}
	if partSize == 0 {
		partSize = 64 * MiB
		switch {
		case size <= GiB:
			partSize = 16 * MiB
		case size > MaxParts*partSize:
			partSize = (size + MaxParts - 1) / MaxParts
			partSize = (partSize + MiB - 1) / MiB * MiB
		}
	}
	if partSize < MinPartSize || partSize > MaxPartSize {
		return Layout{}, invalidf("part size %s is outside the S3 limits (5 MiB to 5 GiB)", FormatBytes(partSize))
	}
	l := Layout{Size: size, PartSize: partSize, Parts: int((size + partSize - 1) / partSize)}
	if l.Parts > MaxParts {
		return Layout{}, invalidf("parts of %s split %s into %d parts, S3 allows %d",
			FormatBytes(partSize), FormatBytes(size), l.Parts, MaxParts)
	}
	return l, nil
}

// rangeLayout splits an object into the ranges of a download (rangeSize 0:
// DefaultRangeSize). An empty object has no ranges.
func rangeLayout(size, rangeSize int64) Layout {
	if rangeSize <= 0 {
		rangeSize = DefaultRangeSize
	}
	return Layout{Size: size, PartSize: rangeSize, Parts: int((size + rangeSize - 1) / rangeSize)}
}

// FormatBytes renders a size with binary units: 512 B, 1.5 KiB, 64 MiB, 2.3 GiB.
func FormatBytes(n int64) string {
	if n < KiB {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	v, i := float64(n)/KiB, 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v == float64(int64(v)) || v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
