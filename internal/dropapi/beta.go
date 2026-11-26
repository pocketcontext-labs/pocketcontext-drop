package dropapi

import "encoding/json"

type BetaDigest struct {
	SHA256 string `json:"sha256"`
}
type BetaCompleteInput struct {
	SHA256   string          `json:"sha256"`
	Attempt  string          `json:"attempt"`
	Outcome  string          `json:"outcome"`
	Manifest json.RawMessage `json:"manifest,omitempty"`
}
type BetaPrepareInput struct {
	SHA256     string `json:"sha256"`
	HTTPPort   int    `json:"http_port,omitempty"`
	Multiplier int    `json:"multiplier"`
}
type BetaManifest struct {
	Schema   string   `json:"schema"`
	App      string   `json:"app"`
	Version  string   `json:"version"`
	HTTPPort int      `json:"http_port"`
	HookPort int      `json:"hook_port"`
	Health   string   `json:"health"`
	Hooks    []string `json:"hooks"`
	Optional []string `json:"optional,omitempty"`
	Requires []string `json:"requires"`
	Minimum  struct {
		MemoryMiB   int `json:"memory_mib"`
		CPUQuarters int `json:"cpu_quarters"`
		DiskMiB     int `json:"disk_mib"`
	} `json:"minimum"`
}
