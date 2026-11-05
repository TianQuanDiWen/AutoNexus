package desktop

// ExistingWindowResult reports whether a uniquely identified existing
// AutoNexus window was restored.
type ExistingWindowResult uint8

const (
	ExistingWindowNotFound ExistingWindowResult = iota
	ExistingWindowRestored
	ExistingWindowAmbiguous
)

type windowCandidate struct {
	hwnd  uintptr
	title string
	pid   uint32
}

func selectUniqueWindow(candidates []windowCandidate, expectedTitle string, ownPID uint32) (uintptr, int) {
	var match uintptr
	count := 0
	for _, candidate := range candidates {
		if candidate.hwnd == 0 || candidate.pid == ownPID || candidate.title != expectedTitle {
			continue
		}
		match = candidate.hwnd
		count++
	}
	if count != 1 {
		return 0, count
	}
	return match, count
}
