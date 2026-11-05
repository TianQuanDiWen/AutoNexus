//go:build windows

package desktop

import (
	"os"
	"reflect"
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

func TestShouldHideOnClose(t *testing.T) {
	for _, tc := range []struct {
		name              string
		trayRegistered    bool
		trayRecoveryReady bool
		isQuitting        bool
		want              bool
	}{
		{name: "registered tray with restart recovery keeps app in background", trayRegistered: true, trayRecoveryReady: true, want: true},
		{name: "missing tray leaves window reachable", trayRegistered: false, trayRecoveryReady: true, want: false},
		{name: "missing restart recovery leaves window reachable", trayRegistered: true, want: false},
		{name: "quitting closes normally", trayRegistered: true, trayRecoveryReady: true, isQuitting: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldHideOnClose(tc.trayRegistered, tc.trayRecoveryReady, tc.isQuitting); got != tc.want {
				t.Fatalf("shouldHideOnClose(%t, %t, %t) = %t, want %t", tc.trayRegistered, tc.trayRecoveryReady, tc.isQuitting, got, tc.want)
			}
		})
	}
}

func TestClipboardCopy(t *testing.T) {
	if os.Getenv("AUTONEXUS_TEST_CLIPBOARD") != "1" {
		t.Skip("manual integration test: set AUTONEXUS_TEST_CLIPBOARD=1 to overwrite the clipboard")
	}
	err := SetClipboardText("http://192.168.1.100:18080")
	if err != nil {
		t.Fatalf("SetClipboardText failed: %v", err)
	}
}

func TestTrayRegistrationRecovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		registered bool
		results    []bool
		operations []uint32
		want       bool
	}{
		{"initial registration", false, []bool{true}, []uint32{NIM_ADD}, true},
		{"existing tray stays registered", true, []bool{true}, []uint32{NIM_MODIFY}, true},
		{"lost Explorer entry is recreated", true, []bool{false, true}, []uint32{NIM_MODIFY, NIM_ADD}, true},
		{"failed recreation remains unavailable", true, []bool{false, false}, []uint32{NIM_MODIFY, NIM_ADD}, false},
		{"initial registration failure", false, []bool{false}, []uint32{NIM_ADD}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := notifyTrayIcon
			t.Cleanup(func() { notifyTrayIcon = original })
			var calls []uint32
			notifyTrayIcon = func(operation uint32, data *NOTIFYICONDATA) (bool, error) {
				calls = append(calls, operation)
				if data.HWnd != 1 || data.HIcon != 1 || data.UID != 1 {
					t.Fatalf("unexpected tray identity: %+v", data)
				}
				if len(calls) > len(tc.results) {
					t.Fatal("unexpected extra tray operation")
				}
				return tc.results[len(calls)-1], nil
			}
			app := &App{hwnd: 1, hIcon: 1, trayRegistered: tc.registered}
			if got := app.ensureTrayIcon(); got != tc.want || app.trayRegistered != tc.want {
				t.Fatalf("registration result=%t stored=%t, want %t", got, app.trayRegistered, tc.want)
			}
			if !reflect.DeepEqual(calls, tc.operations) {
				t.Fatalf("operations=%v, want %v", calls, tc.operations)
			}
		})
	}
}
