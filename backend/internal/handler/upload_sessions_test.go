package handler

import "testing"

func TestAdaptiveMultipartPartSizeStaysWithinStorageLimit(t *testing.T) {
	for _, size := range []int64{16<<20 + 1, 80 << 30, 5 << 40} {
		partSize := adaptiveMultipartPartSize(size)
		parts := (size + partSize - 1) / partSize
		if partSize < 8<<20 || parts > 10_000 {
			t.Fatalf("size=%d part_size=%d parts=%d", size, partSize, parts)
		}
	}
}
