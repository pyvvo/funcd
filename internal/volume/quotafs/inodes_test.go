package quotafs

import "testing"

func TestInodesFor(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name                          string
		size, imageBytes, imageInodes int64
		want                          uint64
	}{
		{"a 64 MiB volume on a 16 KiB-per-inode image", 64 << 20, 10 << 30, (10 << 30) / (16 << 10), 4096},
		{"a share is rounded down", 3, 10, 10, 3},
		{"at least one inode", 1, 1 << 30, 16, 1},
		{"the whole image", 10 << 30, 10 << 30, 655360, 655360},
		{"past the image", 20 << 30, 10 << 30, 655360, 655360},
		{"a product past 64 bits", 1 << 40, 1 << 41, 1 << 30, 1 << 29},
		{"an unknown image", 1 << 20, 0, 0, 1},
	} {
		if got := inodesFor(c.size, c.imageBytes, c.imageInodes); got != c.want {
			t.Errorf("%s: inodesFor(%d, %d, %d) = %d, want %d", c.name, c.size, c.imageBytes, c.imageInodes, got, c.want)
		}
	}
}
