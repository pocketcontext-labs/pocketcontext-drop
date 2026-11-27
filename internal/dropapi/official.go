package dropapi

type OfficialPublish struct {
	SHA256 string `json:"sha256"`
	Prefix string `json:"prefix,omitempty"`
}

type OfficialUpload struct {
	Product string `json:"product"`
	Version string `json:"version"`
}
type CatalogListInput struct {
	AllVersions     bool `json:"all_versions,omitempty"`
	IncludeArchived bool `json:"include_archived,omitempty"`
}
type WorkspaceCreate struct {
	SHA256        string `json:"sha256,omitempty"`
	AllowArchived bool   `json:"allow_archived,omitempty"`
	Product       string `json:"product"`
	Name          string `json:"name"`
	Multiplier    int    `json:"multiplier"`
}
type WorkspaceResize struct {
	App        string `json:"app"`
	Multiplier int    `json:"multiplier"`
}
type WorkspaceUpgrade struct {
	App    string `json:"app"`
	SHA256 string `json:"sha256"`
}
type DirectoryInput struct {
	Search     string `json:"search,omitempty"`
	Status     string `json:"status,omitempty"`
	Role       string `json:"role,omitempty"`
	Workspaces string `json:"workspaces,omitempty"`
	Credits    string `json:"credits,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Offset     int    `json:"offset,omitempty"`
}

// A rehearsal binds observable data invariants to the precise binaries and
// the immutable evidence artifact retained by the release operator.
type OfficialRehearsal struct {
	SHA256         string `json:"sha256"`
	Baseline       string `json:"baseline_sha256"`
	EvidenceURL    string `json:"evidence_url"`
	EvidenceSHA256 string `json:"evidence_sha256"`
	BeforeRecords  string `json:"before_records_sha256"`
	AfterRecords   string `json:"after_records_sha256"`
	BeforeFiles    string `json:"before_attachments_sha256"`
	AfterFiles     string `json:"after_attachments_sha256"`
	Records        int    `json:"records"`
	Attachments    int    `json:"attachments"`
}

type AcceptSubmission struct {
	App     string `json:"app"`
	Release string `json:"release"`
	SHA256  string `json:"sha256"`
}
