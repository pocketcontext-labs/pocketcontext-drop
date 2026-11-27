package render

import "regexp"

var resizeID = regexp.MustCompile("^[0-9a-f]{32}$")

func ValidResizeID(id string) bool { return resizeID.MatchString(id) }

// ResizeReceipt contains only machine state. Runner diagnostic text is never forwarded.
type ResizeReceipt struct {
	RequestID string `json:"requestId"`
	Outcome   string `json:"outcome"`
}

type ObservedResize struct {
	ResizeReceipt
	SizeBytes int64
}

func (r ResizeReceipt) Valid() bool {
	return ValidResizeID(r.RequestID) && (r.Outcome == "applied" || r.Outcome == "refused" || r.Outcome == "pending")
}

// A normal size change is refused. A command may be replayed unchanged, or
// cleared after the local runner has committed it or retained the original.
func volumeTransitionOK(old volume, next Volume, receipt ObservedResize) bool {
	currentBytes := old.SizeBytes
	if prior := old.LocalResize; prior != nil {
		if next.LocalResize != nil && next.LocalResize.RequestID == prior.RequestID {
			return *next.LocalResize == *prior && next.SizeBytes == old.SizeBytes
		}
		if receipt.RequestID != prior.RequestID {
			return false
		}
		switch receipt.Outcome {
		case "applied":
		case "refused":
			currentBytes = prior.FromBytes
		default:
			return false
		}
		if receipt.SizeBytes != currentBytes {
			return false
		}
	}
	if next.LocalResize == nil {
		return currentBytes == next.SizeBytes
	}
	return next.LocalResize.FromBytes == currentBytes
}

func resizeSettled(v volume, r ObservedResize) bool {
	return v.LocalResize != nil && v.LocalResize.RequestID == r.RequestID && ((r.Outcome == "applied" && r.SizeBytes == v.SizeBytes) || (r.Outcome == "refused" && r.SizeBytes == v.LocalResize.FromBytes))
}
