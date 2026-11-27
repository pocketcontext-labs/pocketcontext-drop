package dropapi

const StorageBudgetSchema = "drop.storage-budget/v1"
const StorageBudgetMedia = "application/vnd.pocketcontext.storage-budget+json"
const StorageMarker = "drop-s3/"

// StorageBudget reserves a single S3 exchange against the placed app's owner.
// On completion the same token reduces the hold to observed usage. Provider
// access logs release the hold and post the final charge.
type StorageBudget struct {
	Schema     string `json:"schema"`
	Action     string `json:"action"`
	Token      string `json:"token"`
	App        string `json:"app"`
	Deployment string `json:"deployment"`
	Class      string `json:"class"`
	Bytes      int64  `json:"bytes"`
}
