package hostservices

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Usage classes of requests.
const (
	ClassRead     = "read"
	ClassWrite    = "write"
	ClassList     = "list"
	ClassDelete   = "delete"
	ClassRejected = "rejected"
)

// UsageSchema names the usage file's format.
const UsageSchema = "drop.host-usage/v1"

// Counters are one deployment's monotonic counters since it first used host
// services on this node.
type Counters struct {
	ReadRequests     int64 `json:"readRequests"`
	WriteRequests    int64 `json:"writeRequests"`
	ListRequests     int64 `json:"listRequests"`
	DeleteRequests   int64 `json:"deleteRequests"`
	BudgetRefusals   int64 `json:"budgetRefusals,omitempty"`
	RejectedRequests int64 `json:"rejectedRequests"`
	BytesIn          int64 `json:"bytesIn"`
	BytesOut         int64 `json:"bytesOut"`
}

// DeploymentUsage is one deployment's counters and their sequence number,
// which goes up by one with every persisted change.
type DeploymentUsage struct {
	AppID        string    `json:"appId"`
	DeploymentID string    `json:"deploymentId"`
	Seq          int64     `json:"seq"`
	Counters     Counters  `json:"counters"`
	UpdatedAt    time.Time `json:"updatedAt"`
	changed      bool
}

// UsageFile is the persisted form (0600, written atomically). It holds
// counts only.
type UsageFile struct {
	Schema      string                      `json:"schema"`
	Deployments []*DeploymentUsage          `json:"deployments"`
	MonthOut    map[string]map[string]int64 `json:"monthBytesOut"` // appId → YYYY-MM → bytes
}

// Usage holds every deployment's counters in memory and persists them.
type Usage struct {
	path string
	mu   sync.Mutex
	deps map[string]*DeploymentUsage
	out  map[string]map[string]int64
	nilU DeploymentUsage
}

// usageRef is a handle on one deployment's counters.
type usageRef struct {
	u *Usage
	d *DeploymentUsage
}

// OpenUsage loads path (a missing file starts empty).
func OpenUsage(path string) (*Usage, error) {
	u := &Usage{path: path, deps: map[string]*DeploymentUsage{}, out: map[string]map[string]int64{}}
	if path == "" {
		return u, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return u, nil
	}
	if err != nil {
		return nil, err
	}
	var f UsageFile
	if err := json.Unmarshal(data, &f); err != nil || f.Schema != UsageSchema {
		return nil, errors.New("host services usage file is not a " + UsageSchema + " document")
	}
	for _, d := range f.Deployments {
		if d != nil && d.AppID != "" && d.DeploymentID != "" {
			u.deps[d.DeploymentID] = d
		}
	}
	for app, months := range f.MonthOut {
		u.out[app] = months
	}
	return u, nil
}

// ReadUsageFile reads the file the host-services process writes (for the
// agent's reports); a missing file is empty.
func ReadUsageFile(path string) (*UsageFile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &UsageFile{Schema: UsageSchema}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, errors.New("host services usage file is larger than 16 MiB")
	}
	var f UsageFile
	if err := json.Unmarshal(data, &f); err != nil || f.Schema != UsageSchema {
		return nil, errors.New("host services usage file is not a " + UsageSchema + " document")
	}
	return &f, nil
}

func (u *Usage) deployment(appID, deploymentID string) usageRef {
	if u == nil {
		return usageRef{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	d := u.deps[deploymentID]
	if d == nil {
		d = &DeploymentUsage{AppID: appID, DeploymentID: deploymentID}
		u.deps[deploymentID] = d
	}
	return usageRef{u: u, d: d}
}

func (r usageRef) update(f func(c *Counters)) {
	if r.u == nil {
		return
	}
	r.u.mu.Lock()
	defer r.u.mu.Unlock()
	f(&r.d.Counters)
	r.d.changed = true
	r.d.UpdatedAt = time.Now().UTC().Truncate(time.Second)
}

func (r usageRef) reject() { r.update(func(c *Counters) { c.RejectedRequests++ }) }

func (r usageRef) request(class string, in int64) {
	r.update(func(c *Counters) {
		switch class {
		case ClassRead:
			c.ReadRequests++
		case ClassList:
			c.ListRequests++
		case ClassDelete:
			c.DeleteRequests++
		default:
			c.WriteRequests++
		}
		c.BytesIn += in
	})
}

func month(t time.Time) string { return t.UTC().Format("2006-01") }

func (u *Usage) bytesOut(appID, deploymentID string, n int64, now time.Time) {
	if u == nil || n <= 0 {
		return
	}
	u.deployment(appID, deploymentID).update(func(c *Counters) { c.BytesOut += n })
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.out[appID] == nil {
		u.out[appID] = map[string]int64{}
	}
	u.out[appID][month(now)] += n
}

func (u *Usage) monthOut(appID string, now time.Time) int64 {
	if u == nil {
		return 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.out[appID][month(now)]
}

// Snapshot returns a copy of one deployment's counters.
func (u *Usage) Snapshot(deploymentID string) (DeploymentUsage, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	d, ok := u.deps[deploymentID]
	if !ok {
		return u.nilU, false
	}
	return *d, true
}

// Flush bumps the sequence of every changed deployment and writes the file.
// Deployments idle for 30 days and months older than the previous one are
// dropped.
func (u *Usage) Flush(now time.Time) error {
	u.mu.Lock()
	changed := false
	f := UsageFile{Schema: UsageSchema, MonthOut: map[string]map[string]int64{}}
	for id, d := range u.deps {
		if d.changed {
			d.Seq++
			d.changed = false
			changed = true
		}
		if d.Seq == 0 {
			continue
		}
		if now.Sub(d.UpdatedAt) > 30*24*time.Hour {
			delete(u.deps, id)
			changed = true
			continue
		}
		c := *d
		f.Deployments = append(f.Deployments, &c)
	}
	keep := map[string]bool{month(now): true, month(now.AddDate(0, -1, 0)): true}
	for app, months := range u.out {
		for m := range months {
			if !keep[m] {
				delete(months, m)
				changed = true
			}
		}
		if len(months) > 0 {
			f.MonthOut[app] = map[string]int64{}
			for m, n := range months {
				f.MonthOut[app][m] = n
			}
		}
	}
	u.mu.Unlock()
	if !changed || u.path == "" {
		return nil
	}
	sort.Slice(f.Deployments, func(i, j int) bool { return f.Deployments[i].DeploymentID < f.Deployments[j].DeploymentID })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(u.path, append(data, '\n'))
}

// writeAtomic writes path through a synced temporary file and a rename.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".host-usage-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
