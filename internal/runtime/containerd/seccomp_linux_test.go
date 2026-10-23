package containerd

import (
	"syscall"
	"testing"

	"github.com/opencontainers/runtime-spec/specs-go"
)

// TestScenarioHighBitsDoNotPass: the ioctl rule compares the command under the 32-bit mask the kernel applies, so
// FS_IOC_FSSETXATTR with its upper bits set still matches; the filter allows everything else (ADR-0268).
// scenario: high-bits-do-not-pass
func TestScenarioHighBitsDoNotPass(t *testing.T) {
	s := &specs.Spec{}
	if err := withProjectIDFilter(t.Context(), nil, nil, s); err != nil {
		t.Fatal(err)
	}
	sc := s.Linux.Seccomp
	if sc == nil || sc.DefaultAction != specs.ActAllow || len(sc.Syscalls) != 4 || len(sc.Architectures) < 2 {
		t.Fatalf("filter = %+v, want default allow, native and compat architectures, four rules", sc)
	}
	want := []uint64{0x401C5820, 0x40086602, 0x40046602}
	for i, cmd := range want {
		r := sc.Syscalls[i]
		if len(r.Names) != 1 || r.Names[0] != "ioctl" || r.Action != specs.ActErrno || r.ErrnoRet == nil || *r.ErrnoRet != uint(syscall.EPERM) {
			t.Fatalf("rule %d = %+v, want ioctl EPERM", i, r)
		}
		a := r.Args
		if len(a) != 1 || a[0].Index != 1 || a[0].Op != specs.OpMaskedEqual || a[0].Value != 0xFFFFFFFF || a[0].ValueTwo != cmd {
			t.Fatalf("rule %d args = %+v, want arg 1 masked with 0xFFFFFFFF equal to %#x", i, a, cmd)
		}
		for _, hi := range []uint64{0, 0xFFFFFFFF00000000, 0x0000000100000000} {
			if (cmd|hi)&a[0].Value != a[0].ValueTwo {
				t.Fatalf("command %#x escapes rule %d", cmd|hi, i)
			}
		}
	}
	if fs := sc.Syscalls[3]; len(fs.Names) != 1 || fs.Names[0] != "file_setattr" || fs.Action != specs.ActErrno {
		t.Fatalf("file_setattr rule = %+v", fs)
	}
}
