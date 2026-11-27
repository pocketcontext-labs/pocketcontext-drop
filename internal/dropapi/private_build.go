package dropapi

type PrivateBuildCreate struct {
	SHA256     string `json:"sha256"`
	Name       string `json:"name"`
	Multiplier int    `json:"multiplier"`
}
