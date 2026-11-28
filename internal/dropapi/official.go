package dropapi

type CommitSubmission struct {
	CommitURL string `json:"commit_url"`
}

type OfficialPublish struct {
	UpgradeFrom []string `json:"upgrade_from,omitempty"`
	SHA256      string   `json:"sha256"`
	Prefix      string   `json:"prefix,omitempty"`
}

type OfficialUpload struct {
	CommitURL string `json:"commit_url"`
	Product   string `json:"product"`
	Version   string `json:"version"`
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
