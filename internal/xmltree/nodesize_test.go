package xmltree

import (
	"testing"
	"unsafe"
)

// TestNodeSizeStaysInItsAllocatorClass pins sizeof(Node) so a stray int field
// (or a reordering that reintroduces padding) can't silently push every node
// back into the next Go allocator size class: 160 bytes is a size class of its
// own, 176 and 144 are its neighbours — each byte over a class boundary costs
// the whole next step, on every node in every tree.
func TestNodeSizeStaysInItsAllocatorClass(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("layout budget is for 64-bit targets")
	}
	if got := unsafe.Sizeof(Node{}); got > 160 {
		t.Errorf("sizeof(Node) = %d, want <= 160", got)
	}
	// The streaming reader's element shapes (streamElementSlab) carry their
	// attribute pointers and the slot for the first child in the element's
	// own allocation because, at 152 bytes a node, each shape ends exactly
	// at a size class (160, 320, 480, 640); a node that grew would push
	// every one of them into the next class.
	for _, c := range []struct {
		name string
		size uintptr
		max  uintptr
	}{
		{"streamElem0", unsafe.Sizeof(streamElem0{}), 160},
		{"streamElem1", unsafe.Sizeof(streamElem1{}), 320},
		{"streamElem2", unsafe.Sizeof(streamElem2{}), 480},
		{"streamElem3", unsafe.Sizeof(streamElem3{}), 640},
	} {
		if c.size > c.max {
			t.Errorf("sizeof(%s) = %d, want <= %d", c.name, c.size, c.max)
		}
	}
}
