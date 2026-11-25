package hostservices

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sync"
	"time"
)

// AppsSchema names the file the node agent writes for this process.
const AppsSchema = "drop.host-services/v1"

// AppsFile is <agent state dir>/host-services.json: the apps whose rendered
// nibrunner entry enables host services, with what only Drop knows (bucket,
// region and limits). The agent writes it before desired.json.
type AppsFile struct {
	Schema string     `json:"schema"`
	Apps   []AppEntry `json:"apps"`
}

// AppEntry is one app of AppsFile.
type AppEntry struct {
	AppID        string     `json:"appId"`
	DeploymentID string     `json:"deploymentId"`
	Bucket       string     `json:"bucket"`
	Region       string     `json:"region"`
	Limits       FileLimits `json:"limits"`
}

// FileLimits are Limits in the file's JSON form (zero: default or none).
type FileLimits struct {
	RPS              int   `json:"rps,omitempty"`
	Burst            int   `json:"burst,omitempty"`
	MaxObjectBytes   int64 `json:"maxObjectBytes,omitempty"`
	StoredBytesQuota int64 `json:"storedBytesQuota,omitempty"`
	MonthlyBytesOut  int64 `json:"monthlyBytesOut,omitempty"`
}

var (
	appIDPattern  = regexp.MustCompile(`^d-[a-z0-9]{15}$`)
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
)

// Registry answers Lookup from nibrunner's applied desired.json and the
// agent's host-services.json, read again when either changes (checked at
// most once a second). A deployment is current when both name it, its
// nibrunner entry is not stopped and enables hostServices.s3.
type Registry struct {
	DesiredPath string
	AppsPath    string

	mu      sync.Mutex
	checked time.Time
	stamp   [2]fileStamp
	apps    map[string]App
}

type fileStamp struct {
	size  int64
	mtime time.Time
	ok    bool
}

func stampOf(path string) fileStamp {
	st, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{size: st.Size(), mtime: st.ModTime(), ok: true}
}

// Lookup implements Lookup.
func (r *Registry) Lookup(appID, deploymentID string) (App, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.checked) >= time.Second {
		r.checked = time.Now()
		stamp := [2]fileStamp{stampOf(r.DesiredPath), stampOf(r.AppsPath)}
		if stamp != r.stamp || r.apps == nil {
			apps, err := load(r.DesiredPath, r.AppsPath)
			if err != nil {
				apps = map[string]App{}
			}
			r.apps, r.stamp = apps, stamp
		}
	}
	a, ok := r.apps[appID]
	if !ok || a.DeploymentID != deploymentID {
		return App{}, false
	}
	return a, true
}

// load reads both files; any doubt yields no app at all.
func load(desiredPath, appsPath string) (map[string]App, error) {
	raw, err := os.ReadFile(desiredPath)
	if err != nil {
		return nil, err
	}
	var desired struct {
		Instances []struct {
			AppID        string `json:"appId"`
			DeploymentID string `json:"deploymentId"`
			DesiredState string `json:"desiredState"`
			HostServices *struct {
				S3 bool `json:"s3"`
			} `json:"hostServices"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(raw, &desired); err != nil {
		return nil, err
	}
	running := map[string]string{}
	for _, i := range desired.Instances {
		if i.DesiredState != "stopped" && i.HostServices != nil && i.HostServices.S3 {
			running[i.AppID] = i.DeploymentID
		}
	}
	raw, err = os.ReadFile(appsPath)
	if err != nil {
		return nil, err
	}
	var f AppsFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Schema != AppsSchema {
		return nil, errors.New("invalid host services apps file")
	}
	apps := map[string]App{}
	for _, e := range f.Apps {
		if !appIDPattern.MatchString(e.AppID) || !bucketPattern.MatchString(e.Bucket) || !regionPattern.MatchString(e.Region) ||
			running[e.AppID] != e.DeploymentID || e.DeploymentID == "" {
			continue
		}
		l := e.Limits
		apps[e.AppID] = App{AppID: e.AppID, DeploymentID: e.DeploymentID, Bucket: e.Bucket, Region: e.Region, Limits: Limits{
			RPS: float64(l.RPS), Burst: l.Burst, MaxObjectBytes: l.MaxObjectBytes, StoredBytesQuota: l.StoredBytesQuota, MonthlyBytesOut: l.MonthlyBytesOut,
		}}
	}
	return apps, nil
}

// WriteAppsFile writes the agent's file atomically (0600).
func WriteAppsFile(path string, apps []AppEntry) error {
	if apps == nil {
		apps = []AppEntry{}
	}
	data, err := json.MarshalIndent(AppsFile{Schema: AppsSchema, Apps: apps}, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}
