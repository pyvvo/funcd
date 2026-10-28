package quotafs

import "math/bits"

// inodesFor is a volume's share of the image's inodes, the share its size is of the image's bytes, at least 1
// (ADR-0295). A size of the whole image or more gets every inode.
func inodesFor(sizeBytes, imageBytes, imageInodes int64) uint64 {
	if sizeBytes <= 0 || imageBytes <= 0 || imageInodes <= 0 {
		return 1
	}
	if sizeBytes >= imageBytes {
		return uint64(imageInodes)
	}
	hi, lo := bits.Mul64(uint64(sizeBytes), uint64(imageInodes))
	q, _ := bits.Div64(hi, lo, uint64(imageBytes)) // size < imageBytes, so the quotient fits
	return max(q, 1)
}
