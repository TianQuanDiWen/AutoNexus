//go:build windows

package desktop

import (
	"testing"
	"unsafe"
)

func TestNotifyIconDataSize(t *testing.T) {
	var nid NOTIFYICONDATA
	size := unsafe.Sizeof(nid)
	t.Logf("NOTIFYICONDATA size: %d", size)
	if size < 500 {
		t.Fatalf("NOTIFYICONDATA size unexpectedly small: %d", size)
	}
}

func TestClipboardCopy(t *testing.T) {
	err := SetClipboardText("http://192.168.1.100:18080")
	if err != nil {
		t.Fatalf("SetClipboardText failed: %v", err)
	}
}
