package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The subset of nibrunner's HostDesiredState that Drop writes. The same types
// read the previous document strictly: a field Drop never writes means someone
// else wrote the file, and it is refused.
type document struct {
	HostID      string            `json:"hostId"`
	Revision    string            `json:"revision"`
	Volumes     []volume          `json:"volumes"`
	Instances   []instance        `json:"instances"`
	Checkpoints []json.RawMessage `json:"checkpoints"`
	Exports     []json.RawMessage `json:"exports"`
}

type volume struct {
	VolumeID     string `json:"volumeId"`
	AppID        string `json:"appId"`
	SizeBytes    int64  `json:"sizeBytes"`
	DesiredState string `json:"desiredState"`
}

type instance struct {
	AppID        string      `json:"appId"`
	DeploymentID string      `json:"deploymentId"`
	VolumeID     string      `json:"volumeId"`
	DesiredState string      `json:"desiredState"`
	Activation   *activation `json:"activation,omitempty"`
	Layers       []layer     `json:"layers"`
	Config       config      `json:"config"`
	Hostnames    []hostname  `json:"hostnames"`
	Access       access      `json:"access"`
}

type activation struct {
	SleepWhen sleepWhen `json:"sleepWhen"`
}

type sleepWhen struct {
	Kind      string `json:"kind"`
	TimeoutMs int64  `json:"timeoutMs,omitempty"`
}

type layer struct {
	Kind            string `json:"kind"`
	DestinationPath string `json:"destinationPath"`
	Digest          string `json:"digest"`
	ObjectKey       string `json:"objectKey"`
}

type config struct {
	HTTPPort      int           `json:"httpPort"`
	Command       command       `json:"command"`
	Resources     resources     `json:"resources"`
	HealthCheck   healthCheck   `json:"healthCheck"`
	RestartPolicy restartPolicy `json:"restartPolicy"`
}

type command struct {
	Program          string            `json:"program"`
	Args             []string          `json:"args"`
	WorkingDirectory string            `json:"workingDirectory"`
	Environment      map[string]string `json:"environment"`
}

type resources struct {
	VCPUCount int `json:"vcpuCount"`
	MemoryMib int `json:"memoryMib"`
}

type healthCheck struct {
	Kind               string `json:"kind"`
	Path               string `json:"path,omitempty"`
	IntervalMs         int64  `json:"intervalMs"`
	TimeoutMs          int64  `json:"timeoutMs"`
	GracePeriodMs      int64  `json:"gracePeriodMs"`
	HealthyThreshold   int    `json:"healthyThreshold"`
	UnhealthyThreshold int    `json:"unhealthyThreshold"`
}

type restartPolicy struct {
	MaxRestarts      int     `json:"maxRestarts"`
	InitialBackoffMs int64   `json:"initialBackoffMs"`
	MaxBackoffMs     int64   `json:"maxBackoffMs"`
	BackoffFactor    float64 `json:"backoffFactor"`
	ResetAfterMs     int64   `json:"resetAfterMs"`
}

type hostname struct {
	Hostname string `json:"hostname"`
	Kind     string `json:"kind"`
}

type access struct {
	Kind string `json:"kind"`
}

// previous is the parsed current document, keyed by appId.
type previous struct {
	instances map[string]instance
	volumes   map[string]volume
}

// ForeignAppIDs returns the sorted, distinct appIds in a desired.json document
// (instances, volumes and exports) that do not start with "d-". The agent uses
// it to refuse to start and `drop agent check` to fail. It reads the document
// loosely, so it works on files Drop did not write.
func ForeignAppIDs(doc []byte) ([]string, error) {
	var loose struct {
		Instances []struct {
			AppID *string `json:"appId"`
		} `json:"instances"`
		Volumes []struct {
			AppID *string `json:"appId"`
		} `json:"volumes"`
		Exports []struct {
			AppID *string `json:"appId"`
		} `json:"exports"`
	}
	if err := json.Unmarshal(doc, &loose); err != nil {
		return nil, fmt.Errorf("desired document is not a JSON object with instances, volumes and exports arrays: %w", err)
	}
	seen := map[string]bool{}
	add := func(id *string) {
		if id == nil {
			seen["<missing appId>"] = true
		} else if !strings.HasPrefix(*id, "d-") {
			seen[*id] = true
		}
	}
	for _, e := range loose.Instances {
		add(e.AppID)
	}
	for _, e := range loose.Volumes {
		add(e.AppID)
	}
	for _, e := range loose.Exports {
		add(e.AppID)
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// checkForeign refuses an input whose apps, keep list or previous document
// name any appId not starting with "d-".
func checkForeign(in Input) error {
	seen := map[string]bool{}
	for _, app := range in.Apps {
		if !strings.HasPrefix(app.AppID, "d-") {
			seen[app.AppID] = true
		}
	}
	for _, id := range in.Keep {
		if !strings.HasPrefix(id, "d-") {
			seen[id] = true
		}
	}
	if len(bytes.TrimSpace(in.Previous)) > 0 {
		ids, err := ForeignAppIDs(in.Previous)
		if err != nil {
			return &Error{Code: CodeInvalidPrevious, Message: err.Error()}
		}
		for _, id := range ids {
			seen[id] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return &Error{Code: CodeForeignEntries, Message: "Drop owns the whole desired.json; entries not starting with d- must be removed by the operator", IDs: ids}
}

// parsePrevious reads the current document strictly. Only shapes Drop writes
// are accepted: no checkpoints or exports, no absent entries, no unknown fields.
func parsePrevious(raw json.RawMessage, hostID string) (previous, error) {
	p := previous{instances: map[string]instance{}, volumes: map[string]volume{}}
	if len(bytes.TrimSpace(raw)) == 0 {
		return p, nil
	}
	bad := func(format string, args ...any) error {
		return &Error{Code: CodeInvalidPrevious, Message: fmt.Sprintf(format, args...)}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc document
	if err := dec.Decode(&doc); err != nil {
		return p, bad("not a document Drop wrote: %v", err)
	}
	if dec.More() {
		return p, bad("trailing data after the document")
	}
	if doc.HostID != hostID {
		return p, &Error{Code: CodeHostMismatch, Message: fmt.Sprintf("previous document is for host %q, input for %q", doc.HostID, hostID)}
	}
	if len(doc.Checkpoints) > 0 || len(doc.Exports) > 0 {
		return p, &Error{Code: CodeUnsupportedEntries, Message: "Drop v0 never writes checkpoints or exports; the operator must remove them"}
	}
	for _, v := range doc.Volumes {
		if !reAppID.MatchString(v.AppID) || v.VolumeID != v.AppID+"-vol" {
			return p, bad("volume %q of app %q is not a Drop volume", v.VolumeID, v.AppID)
		}
		if v.DesiredState != "present" {
			return p, &Error{Code: CodeUnsupportedEntries, Message: fmt.Sprintf("volume %q is %q; Drop never writes absent, the operator must remove it", v.VolumeID, v.DesiredState), IDs: []string{v.AppID}}
		}
		if _, dup := p.volumes[v.AppID]; dup {
			return p, bad("app %q has two volumes", v.AppID)
		}
		p.volumes[v.AppID] = v
	}
	for _, inst := range doc.Instances {
		if !reAppID.MatchString(inst.AppID) || inst.VolumeID != inst.AppID+"-vol" || !deploymentIDOK(inst.AppID, inst.DeploymentID) {
			return p, bad("instance %q is not a Drop instance", inst.AppID)
		}
		if _, ok := p.volumes[inst.AppID]; !ok {
			return p, bad("instance %q has no volume", inst.AppID)
		}
		if _, dup := p.instances[inst.AppID]; dup {
			return p, bad("app %q has two instances", inst.AppID)
		}
		p.instances[inst.AppID] = inst
	}
	return p, nil
}
