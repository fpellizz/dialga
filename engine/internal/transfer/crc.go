package transfer

import (
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"hash/crc64"
	"io"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// crc is one of the CRC checksums S3 keeps for whole objects. All three are
// reflected, with the register and the result inverted (the convention of
// hash/crc32 and hash/crc64), so the CRC of a concatenation follows from the
// CRCs of the pieces: parts and ranges are summed in parallel and combined.
type crc struct {
	name   types.ChecksumAlgorithm
	width  uint   // 32 or 64 bits
	poly   uint64 // reflected polynomial
	update func(sum uint64, p []byte) uint64
}

var (
	crc64NVMETable = crc64.MakeTable(0x9a6c9329ac4bc9b5)
	crc32CTable    = crc32.MakeTable(crc32.Castagnoli)

	crc64NVME = crc{types.ChecksumAlgorithmCrc64nvme, 64, 0x9a6c9329ac4bc9b5,
		func(sum uint64, p []byte) uint64 { return crc64.Update(sum, crc64NVMETable, p) }}
	crc32IEEE = crc{types.ChecksumAlgorithmCrc32, 32, crc32.IEEE,
		func(sum uint64, p []byte) uint64 { return uint64(crc32.Update(uint32(sum), crc32.IEEETable, p)) }}
	crc32C = crc{types.ChecksumAlgorithmCrc32c, 32, crc32.Castagnoli,
		func(sum uint64, p []byte) uint64 { return uint64(crc32.Update(uint32(sum), crc32CTable, p)) }}
)

// encode renders a sum as S3 shows it: base64 of the big-endian bytes.
func (c crc) encode(sum uint64) string {
	b := binary.BigEndian.AppendUint64(nil, sum)
	return base64.StdEncoding.EncodeToString(b[8-c.width/8:])
}

// sumReader returns the CRC of everything r yields.
func (c crc) sumReader(r io.Reader, buf []byte) (uint64, error) {
	var sum uint64
	for {
		n, err := r.Read(buf)
		sum = c.update(sum, buf[:n])
		if err == io.EOF {
			return sum, nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// combine returns the CRC of A followed by B from crc(A), crc(B) and the
// length of B: crc(A)·x^(8·len(B)) mod P, xor crc(B) (zlib's crc32_combine).
func (c crc) combine(sumA, sumB uint64, lenB int64) uint64 {
	return c.multiply(c.xPow8n(lenB), sumA) ^ sumB
}

// multiply returns a·b modulo the polynomial, in the reflected bit order
// (the top bit is x^0).
func (c crc) multiply(a, b uint64) uint64 {
	if a == 0 {
		return 0
	}
	m := uint64(1) << (c.width - 1)
	var p uint64
	for {
		if a&m != 0 {
			p ^= b
			if a&(m-1) == 0 {
				return p
			}
		}
		m >>= 1
		if b&1 != 0 {
			b = b>>1 ^ c.poly
		} else {
			b >>= 1
		}
	}
}

// xPow8n returns x^(8·n) modulo the polynomial, by squaring.
func (c crc) xPow8n(n int64) uint64 {
	one := uint64(1) << (c.width - 1)
	base := one >> 1 // x
	for range 3 {
		base = c.multiply(base, base) // x^2, x^4, x^8
	}
	p := one
	for ; n > 0; n >>= 1 {
		if n&1 != 0 {
			p = c.multiply(base, p)
		}
		base = c.multiply(base, base)
	}
	return p
}
