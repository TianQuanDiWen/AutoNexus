package desktop

import "testing"

func TestSelectUniqueWindow(t *testing.T) {
	const title = "AutoNexus 自动化总控平台"
	tests := []struct {
		name       string
		candidates []windowCandidate
		ownPID     uint32
		wantHandle uintptr
		wantCount  int
	}{
		{name: "single exact title", candidates: []windowCandidate{{hwnd: 10, title: title, pid: 7}}, ownPID: 8, wantHandle: 10, wantCount: 1},
		{name: "exclude own process", candidates: []windowCandidate{{hwnd: 10, title: title, pid: 8}}, ownPID: 8},
		{name: "ignore similar title", candidates: []windowCandidate{{hwnd: 10, title: title + " (2)", pid: 7}}, ownPID: 8},
		{name: "ambiguous exact titles", candidates: []windowCandidate{{hwnd: 10, title: title, pid: 7}, {hwnd: 11, title: title, pid: 9}}, ownPID: 8, wantCount: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hwnd, count := selectUniqueWindow(tt.candidates, title, tt.ownPID)
			if hwnd != tt.wantHandle || count != tt.wantCount {
				t.Fatalf("selectUniqueWindow() = (%d, %d), want (%d, %d)", hwnd, count, tt.wantHandle, tt.wantCount)
			}
		})
	}
}
