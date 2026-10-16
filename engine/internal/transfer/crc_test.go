package transfer

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func TestCRCCheckValues(t *testing.T) {
	// The "123456789" check values of the CRC catalogue.
	tests := []struct {
		c    crc
		want uint64
	}{
		{crc64NVME, 0xae8b14860a799888},
		{crc32IEEE, 0xcbf43926},
		{crc32C, 0xe3069283},
	}
	for _, tc := range tests {
		t.Run(string(tc.c.name), func(t *testing.T) {
			got, err := tc.c.sumReader(bytes.NewReader([]byte("123456789")), make([]byte, 4))
			if err != nil || got != tc.want {
				t.Fatalf("sum = %#x, %v; want %#x", got, err, tc.want)
			}
		})
	}
}

func TestCRCCombine(t *testing.T) {
	data := make([]byte, 300_000)
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := range data {
		data[i] = byte(rnd.UintN(256))
	}
	for _, c := range []crc{crc64NVME, crc32IEEE, crc32C} {
		want := c.update(0, data)
		for _, cut := range []int{0, 1, 7, 4096, 150_001, len(data) - 1, len(data)} {
			got := c.combine(c.update(0, data[:cut]), c.update(0, data[cut:]), int64(len(data)-cut))
			if got != want {
				t.Fatalf("%s cut at %d: %#x, want %#x", c.name, cut, got, want)
			}
		}
		// Many pieces, combined left to right as parts are.
		var sum uint64
		for off := 0; off < len(data); off += 33_333 {
			piece := data[off:min(off+33_333, len(data))]
			if off == 0 {
				sum = c.update(0, piece)
				continue
			}
			sum = c.combine(sum, c.update(0, piece), int64(len(piece)))
		}
		if sum != want {
			t.Fatalf("%s pieces: %#x, want %#x", c.name, sum, want)
		}
	}
}

func TestCRCEncode(t *testing.T) {
	// S3 shows checksums as base64 of the big-endian bytes.
	if got := crc32C.encode(0xe3069283); got != "4waSgw==" {
		t.Fatalf("crc32c = %q", got)
	}
	if got := crc64NVME.encode(0xae8b14860a799888); got != "rosUhgp5mIg=" {
		t.Fatalf("crc64nvme = %q", got)
	}
}
