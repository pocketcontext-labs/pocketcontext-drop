package dropapi

const AgentBackupJobsRoute = "/api/drop/agent/backup-jobs"
const BackupDownloadPrefix = "/api/drop/backups/"
const MaxBackupBytes int64 = MaxReleaseBytes

type BackupJob struct {
	ID         string `json:"id" db:"id"`
	Kind       string `json:"kind" db:"kind"`
	AppID      string `json:"app_id" db:"app_id"`
	SourceApp  string `json:"source_app" db:"source_app"`
	InstanceID string `json:"instance_id" db:"instance_id"`
	BackupID   string `json:"backup_id" db:"backup_id"`
	SHA256     string `json:"sha256" db:"sha256"`
	Bytes      int64  `json:"bytes" db:"bytes"`
}

type BackupJobsResult struct {
	Jobs []BackupJob `json:"jobs"`
}

type BackupJobResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type BackupAppInput struct {
	App string `json:"app"`
}

type BackupPolicyInput struct {
	App     string `json:"app"`
	Enabled bool   `json:"enabled"`
}

type BackupRestoreInput struct {
	Backup string `json:"backup"`
	// SameApp requires explicitly confirming the source app's slug. NewName provisions a fresh workspace.
	SameApp string `json:"same_app,omitempty"`
	NewName string `json:"new_name,omitempty"`
}
