package dropapi

import (
	"regexp"
	"time"
)

// Releases and one-time upload links (milestone M5b). A release is one Linux
// amd64 executable of an app, stored once by content in the release store at
// releases/<sha256> (awsx.ReleaseKey). People upload through a one-time link:
// release.upload_link returns a URL that takes one PUT of the file, so the
// file never passes through an operation body (which is at most 256 KiB) or
// an assistant's conversation. release.deploy makes a release the app's
// current one and starts a new deployment.

// Upload limits and the upload route.
const (
	// MaxReleaseBytes is the largest release (90 MiB, decision 2): under
	// Cloudflare's 100 MB request body limit.
	MaxReleaseBytes = 90 << 20
	// UploadLinkTTL is how long an upload link works.
	UploadLinkTTL = 15 * time.Minute
	// UploadRoutePrefix is the path of the upload route before the token:
	// PUT /api/drop/upload/<token>.
	UploadRoutePrefix = "/api/drop/upload/"
	// UploadTokenLength is the length of an upload token.
	UploadTokenLength = 32
	// MaxReleaseNote bounds a release's note.
	MaxReleaseNote = 500
)

// UploadTokenPattern is the form of an upload token: 32 letters and digits
// (about 190 bits). The server stores only its SHA-256.
var UploadTokenPattern = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)

// Release is one stored executable of an app. S3Key is releases/<SHA256>;
// several apps (or uploads) of the same bytes share one stored object.
// Current is true for the app's current release (the one it runs).
type Release struct {
	ID        string `json:"id"`
	App       string `json:"app"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	S3Key     string `json:"s3_key"`
	Note      string `json:"note"`
	CreatedBy string `json:"created_by"`
	Created   string `json:"created"`
	Current   bool   `json:"current"`
}

// ReleaseListInput selects one page of an app's releases, newest first.
type ReleaseListInput struct {
	App    string `json:"app"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

// ReleaseList is a page of an app's releases and the id of its current
// release (empty when it has none).
type ReleaseList struct {
	Releases       []Release `json:"releases"`
	HasMore        bool      `json:"has_more"`
	CurrentRelease string    `json:"current_release"`
}

// ReleaseUploadLinkInput asks for a one-time upload link. Note is kept with
// the release; Activate deploys the release as soon as the upload completes.
type ReleaseUploadLinkInput struct {
	App      string `json:"app"`
	Note     string `json:"note,omitempty"`
	Activate bool   `json:"activate,omitempty"`
}

// UploadLink is a one-time upload link. URL (on the server's public origin)
// and Path (the same route without the origin) carry the token: shown once,
// never stored, valid for one successful PUT of at most MaxBytes until
// ExpiresAt. Example is a curl command line for it.
type UploadLink struct {
	URL       string `json:"url"`
	Path      string `json:"path"`
	Method    string `json:"method"`
	MaxBytes  int64  `json:"max_bytes"`
	ExpiresAt string `json:"expires_at"`
	Activate  bool   `json:"activate"`
	Example   string `json:"example"`
}

// ReleaseDeployInput makes a release (by id) the app's current release.
type ReleaseDeployInput struct {
	App     string `json:"app"`
	Release string `json:"release"`
}

// ReleaseDeployResult is the app after release.deploy and whether its
// deployment number went up (false when the release was already current).
type ReleaseDeployResult struct {
	App              App     `json:"app"`
	Release          Release `json:"release"`
	DeploymentBumped bool    `json:"deployment_bumped"`
	DeploymentID     string  `json:"deployment_id"`
}

// UploadResult is the answer of a successful PUT to an upload link. Created
// is false when the app already had a release of the same bytes (the link is
// used all the same); Deployed is true when the link activated the release
// and it was not already current.
type UploadResult struct {
	Release      Release `json:"release"`
	Created      bool    `json:"created"`
	Deployed     bool    `json:"deployed"`
	DeploymentID string  `json:"deployment_id"`
}

// Error codes of releases and the upload route: 409 release codes, 413
// release_too_large, 400 invalid_release (not a linux/amd64 ELF executable),
// 404 upload_link_invalid (unknown, used or expired link), 429 rate_limited,
// 502 release_store_failed, 503 releases_unavailable.
const (
	CodeReleaseTooLarge     = "release_too_large"
	CodeInvalidRelease      = "invalid_release"
	CodeUploadLinkInvalid   = "upload_link_invalid"
	CodeUploadInProgress    = "upload_in_progress"
	CodeRateLimited         = "rate_limited"
	CodeReleaseStoreFailed  = "release_store_failed"
	CodeReleasesUnavailable = "releases_unavailable"
)

// ELFHeaderBytes is how much of a file ReleaseHeaderProblem reads.
const ELFHeaderBytes = 64

// ReleaseHeaderProblem checks the first ELFHeaderBytes of a release: a
// 64-bit little-endian ELF executable (or position-independent executable)
// for x86-64, with the System V or Linux ABI, as nibrunner runs on Drop's
// linux/amd64 hosts. It returns "" for a good header, else why not. The
// server checks every upload; the CLI and the reader check first to fail
// fast.
func ReleaseHeaderProblem(h []byte) string {
	const want = "A release is a linux/amd64 ELF executable (for Go: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build)"
	if len(h) < ELFHeaderBytes || string(h[:4]) != "\x7fELF" {
		return want + "; this file is not an ELF executable."
	}
	if h[4] != 2 || h[5] != 1 || h[6] != 1 {
		return want + "; this ELF file is not 64-bit little-endian."
	}
	if h[7] != 0 && h[7] != 3 {
		return want + "; this ELF file is for another operating system."
	}
	if t := uint16(h[16]) | uint16(h[17])<<8; t != 2 && t != 3 {
		return want + "; this ELF file is not an executable (it may be an object file or a core dump)."
	}
	if m := uint16(h[18]) | uint16(h[19])<<8; m != 0x3E {
		arch := "another architecture"
		switch m {
		case 0xB7:
			arch = "arm64"
		case 0x03:
			arch = "386"
		case 0xF3:
			arch = "riscv64"
		}
		return want + "; this one is built for " + arch + "."
	}
	return ""
}
