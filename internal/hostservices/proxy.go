package hostservices

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"golang.org/x/time/rate"
)

// App is one app the proxy serves: its current deployment, replica bucket
// and limits, as the node agent last applied them.
type App struct {
	AppID        string
	DeploymentID string
	Bucket       string
	Region       string
	Limits       Limits
}

// Limits bound one app. Zero quota and budget mean no limit.
type Limits struct {
	RPS              float64
	Burst            int
	MaxObjectBytes   int64
	StoredBytesQuota int64
	MonthlyBytesOut  int64
}

// Default limits when the desired document names none.
const (
	DefaultRPS            = 20
	DefaultBurst          = 200
	DefaultMaxObjectBytes = 100 << 20
)

// WithDefaults fills unset rate, burst and object size.
func (l Limits) WithDefaults() Limits {
	if l.RPS <= 0 {
		l.RPS = DefaultRPS
	}
	if l.Burst <= 0 {
		l.Burst = DefaultBurst
	}
	if l.MaxObjectBytes <= 0 {
		l.MaxObjectBytes = DefaultMaxObjectBytes
	}
	return l
}

// Credentials returns the app's own AWS session.
type Credentials interface {
	Credentials(ctx context.Context, appID string) (aws.Credentials, error)
}

// Proxy is the S3 proxy shared by every app on the node.
type Proxy struct {
	Upstream Upstream
	Creds    Credentials
	Usage    *Usage
	Now      func() time.Time
	Logf     func(string, ...any)

	mu        sync.Mutex
	limiters  map[string]*limiter
	stored    map[string]*inventory
	uploads   map[string]map[int]int64 // uploadId → part → bytes
	uploadKey map[string]string        // uploadId → appId
}

type limiter struct {
	rps   float64
	burst int
	l     *rate.Limiter
}

func (p *Proxy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Proxy) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// allow takes one token from the app's bucket.
func (p *Proxy) allow(app App) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.limiters == nil {
		p.limiters = map[string]*limiter{}
	}
	l := p.limiters[app.AppID]
	if l == nil || l.rps != app.Limits.RPS || l.burst != app.Limits.Burst {
		l = &limiter{rps: app.Limits.RPS, burst: app.Limits.Burst, l: rate.NewLimiter(rate.Limit(app.Limits.RPS), app.Limits.Burst)}
		p.limiters[app.AppID] = l
	}
	return l.l.AllowN(p.now(), 1)
}

// ServeApp serves one guest request for app.
func (p *Proxy) ServeApp(w http.ResponseWriter, r *http.Request, app App) {
	app.Limits = app.Limits.WithDefaults()
	prefix := app.AppID + "/"
	usage := p.Usage.deployment(app.AppID, app.DeploymentID)
	fail := func(e *s3Error) {
		usage.reject()
		writeError(w, e)
	}
	if !p.allow(app) {
		fail(refuse(http.StatusServiceUnavailable, "SlowDown", "Please reduce your request rate."))
		return
	}
	req, e := parseRequest(r, app.Bucket, prefix)
	if e != nil {
		fail(e)
		return
	}
	pl, e := preparePayload(r)
	if e != nil {
		fail(e)
		return
	}
	in := &countingReader{r: pl.body}
	pl.body = in
	var deleted []string
	switch req.op {
	case OpPutObject, OpUploadPart:
		req.size = pl.decoded
		if e := p.admitWrite(r.Context(), app, req); e != nil {
			fail(e)
			return
		}
	case OpDeleteObjects:
		raw, keys, e := readDeleteKeys(pl.body, prefix)
		if e != nil {
			fail(e)
			return
		}
		deleted = keys
		pl.body = bytes.NewReader(raw)
		pl.length = int64(len(raw))
	case OpCompleteUpload:
		raw, err := io.ReadAll(io.LimitReader(pl.body, maxXMLBody+1))
		if err != nil || len(raw) > maxXMLBody {
			fail(refuse(http.StatusBadRequest, "MalformedXML", "the part list is too large"))
			return
		}
		pl.body = bytes.NewReader(raw)
		pl.length = int64(len(raw))
	case OpGetObject:
		if app.Limits.MonthlyBytesOut > 0 && p.Usage.monthOut(app.AppID, p.now()) >= app.Limits.MonthlyBytesOut {
			fail(refuse(http.StatusForbidden, "DownloadBudgetExceeded", "The app's monthly download budget is used up."))
			return
		}
	}
	creds, err := p.Creds.Credentials(r.Context(), app.AppID)
	if err != nil {
		p.logf("host services: %s: credentials: %v", app.AppID, err)
		fail(refuse(http.StatusServiceUnavailable, "ServiceUnavailable", "Credentials are temporarily unavailable."))
		return
	}
	out, err := p.Upstream.build(r.Context(), r, req, pl, app.Bucket, app.Region, creds, p.now())
	if err != nil {
		fail(refuse(http.StatusInternalServerError, "InternalError", "The request could not be signed."))
		return
	}
	client := p.Upstream.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(out)
	usage.request(req.op.Class(), in.n)
	if err != nil {
		if r.Context().Err() == nil {
			p.logf("host services: %s: %s: %v", app.AppID, req.op, err)
		}
		writeError(w, refuse(http.StatusBadGateway, "ServiceUnavailable", "S3 could not be reached."))
		return
	}
	defer resp.Body.Close()
	if req.op == OpGetObject && resp.StatusCode/100 == 2 && app.Limits.MonthlyBytesOut > 0 &&
		resp.ContentLength > 0 && p.Usage.monthOut(app.AppID, p.now())+resp.ContentLength > app.Limits.MonthlyBytesOut {
		usage.reject()
		writeError(w, refuse(http.StatusForbidden, "DownloadBudgetExceeded", "The app's monthly download budget does not cover this object."))
		return
	}
	for name, values := range resp.Header {
		switch name {
		case "Connection", "Keep-Alive", "Transfer-Encoding", "Proxy-Authenticate", "Upgrade":
			continue
		}
		w.Header()[name] = values
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	p.Usage.bytesOut(app.AppID, app.DeploymentID, n, p.now())
	p.afterResponse(app, req, resp.StatusCode, deleted)
}

// admitWrite checks the object size cap and the stored-bytes quota before
// any byte is sent.
func (p *Proxy) admitWrite(ctx context.Context, app App, req *s3Request) *s3Error {
	if req.size > app.Limits.MaxObjectBytes {
		return refuse(http.StatusBadRequest, "EntityTooLarge", "The object exceeds the app's maximum object size.")
	}
	var uploadTotal int64
	if req.op == OpUploadPart {
		p.mu.Lock()
		if owner, ok := p.uploadKey[req.upload]; ok && owner != app.AppID {
			p.mu.Unlock()
			return accessDenied("unknown upload")
		}
		for part, n := range p.uploads[req.upload] {
			if part != req.part {
				uploadTotal += n
			}
		}
		p.mu.Unlock()
		if uploadTotal+req.size > app.Limits.MaxObjectBytes {
			return refuse(http.StatusBadRequest, "EntityTooLarge", "The object exceeds the app's maximum object size.")
		}
	}
	if app.Limits.StoredBytesQuota <= 0 {
		return nil
	}
	stored, existing, err := p.storedBytes(ctx, app, req.key)
	if err != nil {
		p.logf("host services: %s: inventory: %v", app.AppID, err)
		return refuse(http.StatusServiceUnavailable, "ServiceUnavailable", "Stored bytes are temporarily unknown.")
	}
	if req.op == OpUploadPart {
		existing = 0
	}
	if stored-existing+uploadTotal+req.size > app.Limits.StoredBytesQuota {
		return refuse(http.StatusForbidden, "QuotaExceeded", "The app's stored-bytes quota is used up.")
	}
	return nil
}

// createResult is the part of CreateMultipartUpload's answer the proxy reads.
type createResult struct {
	UploadID string `xml:"UploadId"`
}

// afterResponse keeps the inventory and multipart bookkeeping current.
func (p *Proxy) afterResponse(app App, req *s3Request, status int, deleted []string) {
	if status/100 != 2 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	inv := p.stored[app.AppID]
	switch req.op {
	case OpPutObject:
		if inv != nil {
			inv.put(req.key, req.size)
		}
	case OpUploadPart:
		if p.uploads == nil {
			p.uploads, p.uploadKey = map[string]map[int]int64{}, map[string]string{}
		}
		if p.uploads[req.upload] == nil {
			p.uploads[req.upload] = map[int]int64{}
		}
		p.uploads[req.upload][req.part] = req.size
		p.uploadKey[req.upload] = app.AppID
	case OpCompleteUpload, OpAbortUpload:
		var total int64
		for _, n := range p.uploads[req.upload] {
			total += n
		}
		delete(p.uploads, req.upload)
		delete(p.uploadKey, req.upload)
		if inv != nil {
			if req.op == OpCompleteUpload {
				inv.put(req.key, total)
			}
			inv.dirty = true
		}
	case OpDeleteObject:
		if inv != nil {
			inv.remove(req.key)
		}
	case OpDeleteObjects:
		if inv != nil {
			for _, k := range deleted {
				inv.remove(k)
			}
			inv.dirty = true
		}
	}
}

// inventory is the app's stored objects, listed from S3 and kept current by
// the proxy's own writes. It is listed again every 15 minutes, and a minute
// after an operation whose effect the proxy cannot know exactly.
type inventory struct {
	objects  map[string]int64
	total    int64
	listedAt time.Time
	dirty    bool
}

const (
	inventoryTTL      = 15 * time.Minute
	inventoryDirtyTTL = time.Minute
)

func (i *inventory) put(key string, size int64) {
	i.total += size - i.objects[key]
	i.objects[key] = size
}

func (i *inventory) remove(key string) {
	i.total -= i.objects[key]
	delete(i.objects, key)
}

func (p *Proxy) inventoryLocked(appID string) *inventory {
	if p.stored == nil {
		p.stored = map[string]*inventory{}
	}
	return p.stored[appID]
}

// storedBytes returns the app's stored total and the current size of key,
// listing the prefix first when the inventory is missing or due.
func (p *Proxy) storedBytes(ctx context.Context, app App, key string) (total, existing int64, err error) {
	p.mu.Lock()
	inv := p.inventoryLocked(app.AppID)
	now := p.now()
	due := inv == nil || now.Sub(inv.listedAt) >= inventoryTTL || (inv.dirty && now.Sub(inv.listedAt) >= inventoryDirtyTTL)
	p.mu.Unlock()
	if due {
		listed, err := p.list(ctx, app)
		p.mu.Lock()
		switch {
		case err == nil:
			listed.listedAt = now
			p.stored[app.AppID] = listed
		case p.stored[app.AppID] == nil:
			p.mu.Unlock()
			return 0, 0, err
		}
		// A stale listing still bounds the app better than none.
		p.mu.Unlock()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	inv = p.stored[app.AppID]
	return inv.total, inv.objects[key], nil
}

type listResult struct {
	Contents []struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

// list reads every object under the app's prefix with the app's own session.
func (p *Proxy) list(ctx context.Context, app App) (*inventory, error) {
	inv := &inventory{objects: map[string]int64{}}
	token := ""
	for page := 0; page < 10000; page++ {
		creds, err := p.Creds.Credentials(ctx, app.AppID)
		if err != nil {
			return nil, err
		}
		q := [][2]string{{"list-type", "2"}, {"prefix", app.AppID + "/"}}
		if token != "" {
			q = append(q, [2]string{"continuation-token", token})
		}
		in, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://local/", nil)
		out, err := p.Upstream.build(ctx, in, &s3Request{op: OpListObjectsV2, query: q}, &payload{hash: emptySHA256, length: 0}, app.Bucket, app.Region, creds, p.now())
		if err != nil {
			return nil, err
		}
		client := p.Upstream.Client
		if client == nil {
			client = http.DefaultClient
		}
		resp, err := client.Do(out)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, errors.New("list answered " + strconv.Itoa(resp.StatusCode))
		}
		var res listResult
		if err := xml.Unmarshal(body, &res); err != nil {
			return nil, err
		}
		for _, c := range res.Contents {
			inv.put(c.Key, c.Size)
		}
		if !res.IsTruncated || res.NextContinuationToken == "" {
			return inv, nil
		}
		token = res.NextContinuationToken
	}
	return nil, errors.New("listing too long")
}
