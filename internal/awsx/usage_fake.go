package awsx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// MemUsageStore is an in-memory UsageStore for unit tests: log objects put
// with PutObject, and metrics filters. FailMetrics makes every metrics call
// fail with that error.
type MemUsageStore struct {
	mu          sync.Mutex
	objects     map[string][]byte
	filters     map[string]string
	FailMetrics error
	// Calls counts metrics calls ("put d-…", "delete d-…", "list").
	Calls []string
}

// NewMemUsageStore returns an empty in-memory usage store.
func NewMemUsageStore() *MemUsageStore {
	return &MemUsageStore{objects: map[string][]byte{}, filters: map[string]string{}}
}

// PutObject stores a log object (tests).
func (s *MemUsageStore) PutObject(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), data...)
}

func (s *MemUsageStore) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateLogKey(prefix); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ObjectInfo
	for k, v := range s.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, ObjectInfo{Key: k, Size: int64(len(v))})
		}
	}
	slices.SortFunc(out, func(a, b ObjectInfo) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

func (s *MemUsageStore) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateLogKey(key); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *MemUsageStore) metricsCall(call, id string) error {
	s.Calls = append(s.Calls, strings.TrimSpace(call+" "+id))
	if s.FailMetrics != nil {
		return s.FailMetrics
	}
	if call != "list" && !ValidMetricsID(id) {
		return fmt.Errorf("%w: metrics id %q", ErrInvalid, id)
	}
	return nil
}

func (s *MemUsageStore) PutPrefixFilter(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.metricsCall("put", id); err != nil {
		return err
	}
	s.filters[id] = id + "/"
	return nil
}

func (s *MemUsageStore) DeleteFilter(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.metricsCall("delete", id); err != nil {
		return err
	}
	delete(s.filters, id)
	return nil
}

func (s *MemUsageStore) ListFilters(ctx context.Context) ([]MetricsFilter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.metricsCall("list", ""); err != nil {
		return nil, err
	}
	out := make([]MetricsFilter, 0, len(s.filters))
	for id, p := range s.filters {
		out = append(out, MetricsFilter{ID: id, Prefix: p})
	}
	slices.SortFunc(out, func(a, b MetricsFilter) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Filters returns a copy of the filters (tests).
func (s *MemUsageStore) Filters() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.filters {
		out[k] = v
	}
	return out
}

// DirUsageStore is the directory fake of `--fake-aws DIR`:
//
//	DIR/usage/logs/<key>      one file per log bucket object (tests write them)
//	DIR/usage/metrics/<id>    one file per metrics filter, holding its prefix
//
// Every access goes through os.Root, like the other directory fakes.
type DirUsageStore struct{ logs, metrics string }

// NewDirUsageStore creates dir/logs and dir/metrics (0700) if needed.
func NewDirUsageStore(dir string) (*DirUsageStore, error) {
	s := &DirUsageStore{logs: filepath.Join(dir, "logs"), metrics: filepath.Join(dir, "metrics")}
	for _, d := range []string{s.logs, s.metrics} {
		if err := ensureDir(d); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *DirUsageStore) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateLogKey(prefix); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.logs)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []ObjectInfo
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && strings.HasPrefix(p, prefix) && !strings.HasPrefix(path.Base(p), ".tmp-") {
			info, err := d.Info()
			if err != nil {
				return err
			}
			out = append(out, ObjectInfo{Key: p, Size: info.Size()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b ObjectInfo) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

func (s *DirUsageStore) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateLogKey(key); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.logs)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || isNotDir(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, err
	}
	return f, nil
}

func (s *DirUsageStore) PutPrefixFilter(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidMetricsID(id) {
		return fmt.Errorf("%w: metrics id %q", ErrInvalid, id)
	}
	root, err := os.OpenRoot(s.metrics)
	if err != nil {
		return err
	}
	defer root.Close()
	tmp := tempName()
	defer root.Remove(tmp)
	if err := root.WriteFile(tmp, []byte(id+"/"), 0o600); err != nil {
		return err
	}
	return root.Rename(tmp, id)
}

func (s *DirUsageStore) DeleteFilter(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ValidMetricsID(id) {
		return fmt.Errorf("%w: metrics id %q", ErrInvalid, id)
	}
	root, err := os.OpenRoot(s.metrics)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(id); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *DirUsageStore) ListFilters(ctx context.Context) ([]MetricsFilter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.metrics)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.metrics)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []MetricsFilter
	for _, e := range entries {
		if !e.Type().IsRegular() || !ValidMetricsID(e.Name()) {
			continue
		}
		data, err := root.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, MetricsFilter{ID: e.Name(), Prefix: string(data)})
	}
	return out, nil
}
