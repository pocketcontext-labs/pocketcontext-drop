package agent

import (
	"encoding/json"
	"fmt"
	"os"
)

// StateSchema names the state file's format.
const StateSchema = "drop.agent-state/v1"

// State is the agent's state file, <state dir>/state.json (0600, written
// atomically). It holds metadata only: never a secret value, the node
// secret or a token (TestStateFileHoldsNoSecret). The agent can lose it: on
// start a missing or unreadable file is rebuilt from desired.json, which
// stays the truth about what runs.
type State struct {
	Schema string `json:"schema"`
	// Node and NodeID name the node the agent signs in as; HostID is
	// nibrunner's hostId from desired.json.
	Node   string `json:"node"`
	NodeID string `json:"nodeId"`
	HostID string `json:"hostId"`
	// LastSeq is the desiredSeq of the last document applied (written, or
	// found unchanged); LastRevision and LastContentHash describe the
	// desired.json the agent last wrote or confirmed.
	LastSeq         int64  `json:"lastSeq"`
	LastRevision    string `json:"lastRevision"`
	LastContentHash string `json:"lastContentHash"`
	// Times in RFC 3339 (UTC); empty until the first one.
	LastPullAt   string `json:"lastPullAt"`
	LastWriteAt  string `json:"lastWriteAt"`
	LastReportAt string `json:"lastReportAt"`
	// LastError is the last failure (metadata only: codes, appIds, parameter
	// names), empty after a successful pull.
	LastError string `json:"lastError"`
	// Apps are the appIds in desired.json (all start with d-: Drop owns the
	// whole file), with the agent's phase for each app it was last given.
	Apps []StateApp `json:"apps"`
	// Artifacts are staged releases by sha256 (size and mtime at the last
	// verification).
	Artifacts map[string]ArtifactInfo `json:"artifacts"`
}

// StateApp is one app in the state file.
type StateApp struct {
	AppID        string `json:"appId"`
	DeploymentID string `json:"deploymentId"`
	// Phase is the agent's own verdict: rendered, kept (a previous entry was
	// kept), staging_failed, secret_failed, failed, or tombstoned.
	Phase   string `json:"phase"`
	Message string `json:"message,omitempty"`
}

// loadState reads the state file. A missing file gives an empty state; an
// unreadable or foreign one is reported and replaced by an empty state.
func loadState(path string) (State, error) {
	empty := State{Schema: StateSchema, Artifacts: map[string]ArtifactInfo{}}
	data, err := readOptional(path)
	if err != nil {
		return empty, err
	}
	if data == nil {
		return empty, nil
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil || s.Schema != StateSchema {
		return empty, fmt.Errorf("state file %s is not a %s document; rebuilding it", path, StateSchema)
	}
	if s.Artifacts == nil {
		s.Artifacts = map[string]ArtifactInfo{}
	}
	return s, nil
}

// saveState writes the state file atomically (0600) in a 0700 directory.
func saveState(dir, path string, s State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}
