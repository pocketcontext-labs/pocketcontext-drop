// Package usage turns the replica bucket's S3 server access logs and daily
// S3 Inventory into per-app usage: a strict access-log parser, the request
// classes S3 bills, attribution to an app by key prefix, inventory totals,
// and the limits and cost estimate Drop shows. It is pure: no clock, no
// network, no database.
//
// Attribution. Every guest's STS session may touch only keys under
// "<appId>/" (IAM enforces the prefix), so S3's own record of a request's key
// (or, for a listing, its prefix parameter) names the app that made it. A
// request S3 refused with 401 or 403 proves nothing about who sent it (anyone
// can ask for another app's key and be denied), so it is counted only as
// denied and never in the request classes, bytes or limits.
package usage

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pocketcontext-labs/pocketcontext-drop/internal/appspec"
)

// Line is one parsed access-log record: the fields Drop uses.
type Line struct {
	Time       time.Time
	Operation  string // REST.GET.OBJECT, BATCH.DELETE.OBJECT, S3.EXPIRE.OBJECT, ...
	Key        string // URL-encoded object key, "" for "-"
	RequestURI string // "GET /key HTTP/1.1", "" for "-"
	Status     int
	BytesSent  int64
	ObjectSize int64
}

// minFields is the number of fields up to and including the version id;
// S3 appends newer fields at the end, which the parser ignores.
const minFields = 18

// maxLineBytes bounds one log line; longer lines are malformed.
const maxLineBytes = 64 << 10

// ErrMalformed reports a line that is not an S3 server access-log record.
var ErrMalformed = errors.New("malformed access-log line")

// splitFields splits a log line into fields: separated by single spaces,
// "[...]" and "\"...\"" are one field each (quotes and brackets removed).
// A quoted field may hold \" escapes; it must end with a quote followed by
// a space or the end of the line.
func splitFields(line string) ([]string, error) {
	var out []string
	for i := 0; i < len(line); {
		switch line[i] {
		case ' ':
			return nil, ErrMalformed // empty field: two spaces in a row or a leading space
		case '[':
			end := strings.IndexByte(line[i:], ']')
			if end < 0 {
				return nil, ErrMalformed
			}
			out = append(out, line[i+1:i+end])
			i += end + 1
		case '"':
			var b strings.Builder
			j := i + 1
			closed := false
			for j < len(line) {
				c := line[j]
				if c == '\\' && j+1 < len(line) && (line[j+1] == '"' || line[j+1] == '\\') {
					b.WriteByte(line[j+1])
					j += 2
					continue
				}
				if c == '"' {
					closed = true
					j++
					break
				}
				b.WriteByte(c)
				j++
			}
			if !closed {
				return nil, ErrMalformed
			}
			out = append(out, b.String())
			i = j
		default:
			end := strings.IndexByte(line[i:], ' ')
			if end < 0 {
				end = len(line) - i
			}
			field := line[i : i+end]
			if strings.ContainsAny(field, "\"[]") {
				return nil, ErrMalformed
			}
			out = append(out, field)
			i += end
		}
		if i < len(line) {
			if line[i] != ' ' || i+1 == len(line) {
				return nil, ErrMalformed
			}
			i++
		}
	}
	return out, nil
}

func dash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

func count(s string) (int64, error) {
	if s == "-" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, ErrMalformed
	}
	return n, nil
}

// ParseLine parses one access-log record strictly. A line with too few
// fields, an unparsable time, status or byte count is ErrMalformed.
func ParseLine(line string) (Line, error) {
	line = strings.TrimRight(line, "\r")
	f, err := splitFields(line)
	if err != nil || len(f) < minFields {
		return Line{}, ErrMalformed
	}
	t, err := time.Parse("02/Jan/2006:15:04:05 -0700", f[2])
	if err != nil {
		return Line{}, ErrMalformed
	}
	op := f[6]
	if op == "" || op == "-" || strings.ContainsAny(op, " /") {
		return Line{}, ErrMalformed
	}
	status, err := strconv.Atoi(f[9])
	if err != nil || status < 100 || status > 599 {
		return Line{}, ErrMalformed
	}
	sent, err := count(f[11])
	if err != nil {
		return Line{}, err
	}
	size, err := count(f[12])
	if err != nil {
		return Line{}, err
	}
	return Line{Time: t.UTC(), Operation: op, Key: dash(f[7]), RequestURI: dash(f[8]), Status: status, BytesSent: sent, ObjectSize: size}, nil
}

// Class is how S3 bills a request.
type Class int

const (
	// Ignored are records S3 makes itself (lifecycle expirations, the source
	// side of a copy) that are not billed as requests.
	Ignored Class = iota
	// ClassA: PUT, COPY, POST, LIST and the multipart create, part and
	// complete requests.
	ClassA
	// ClassB: GET, HEAD and every other request.
	ClassB
	// Delete: DELETE requests (free, but counted against the request limit
	// with Class A).
	Delete
)

// listOperations are the bucket-level reads S3 bills as LIST (Class A).
var listOperations = map[string]bool{
	"REST.GET.BUCKET":         true, // ListObjects(V2)
	"REST.GET.BUCKETVERSIONS": true, // ListObjectVersions
	"REST.GET.UPLOADS":        true, // ListMultipartUploads
	"REST.GET.UPLOAD":         true, // ListParts
}

// Classify returns the billing class of an operation.
func Classify(op string) Class {
	switch {
	case op == "BATCH.DELETE.OBJECT":
		return Delete
	case op == "REST.COPY.OBJECT_GET":
		return Ignored
	case !strings.HasPrefix(op, "REST."):
		return Ignored
	case strings.HasPrefix(op, "REST.DELETE."):
		return Delete
	case strings.HasPrefix(op, "REST.PUT."), strings.HasPrefix(op, "REST.POST."), strings.HasPrefix(op, "REST.COPY."), listOperations[op]:
		return ClassA
	default:
		return ClassB
	}
}

// uploadOperations carry the uploaded bytes in the object size field.
var uploadOperations = map[string]bool{"REST.PUT.OBJECT": true, "REST.PUT.PART": true, "REST.POST.OBJECT": true}

// AppOf returns the app id a record belongs to, or "": the first segment of
// its key, or of a listing's prefix parameter, when it is an app id followed
// by "/".
func AppOf(l Line) string {
	if l.Key != "" {
		key, err := url.PathUnescape(l.Key)
		if err != nil {
			return ""
		}
		return appPrefix(key)
	}
	if !listOperations[l.Operation] {
		return ""
	}
	parts := strings.Split(l.RequestURI, " ")
	if len(parts) != 3 {
		return ""
	}
	_, rawQuery, ok := strings.Cut(parts[1], "?")
	if !ok {
		return ""
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil || len(query["prefix"]) != 1 {
		return ""
	}
	return appPrefix(query.Get("prefix"))
}

func appPrefix(key string) string {
	id, _, ok := strings.Cut(key, "/")
	if !ok || !appspec.AppIDPattern.MatchString(id) {
		return ""
	}
	return id
}

// Counts are one app's usage in one hour (or summed over a period).
type Counts struct {
	ClassA    int64 `json:"class_a"`
	ClassB    int64 `json:"class_b"`
	Deletes   int64 `json:"deletes"`
	BytesIn   int64 `json:"bytes_in"`
	BytesOut  int64 `json:"bytes_out"`
	Status4xx int64 `json:"status_4xx"`
	Status5xx int64 `json:"status_5xx"`
	Denied    int64 `json:"denied"`
}

// Add adds o to c.
func (c *Counts) Add(o Counts) {
	c.ClassA += o.ClassA
	c.ClassB += o.ClassB
	c.Deletes += o.Deletes
	c.BytesIn += o.BytesIn
	c.BytesOut += o.BytesOut
	c.Status4xx += o.Status4xx
	c.Status5xx += o.Status5xx
	c.Denied += o.Denied
}

// IsZero reports whether nothing was counted.
func (c Counts) IsZero() bool { return c == Counts{} }

// HourKey is one app in one UTC hour.
type HourKey struct {
	App  string // app id, d-…
	Hour string // 2006-01-02T15:00:00Z
}

// HourOf formats the UTC hour of t.
func HourOf(t time.Time) string { return t.UTC().Truncate(time.Hour).Format("2006-01-02T15:00:00Z") }

// Summary is what one log object contributed.
type Summary struct {
	Lines        int // records read, malformed ones included
	Malformed    int // lines that are not access-log records, skipped
	Unattributed int // records whose key or prefix names no app
	Ignored      int // records S3 does not bill as requests
	Hours        map[HourKey]*Counts
}

// Record adds one parsed record to the summary.
func (s *Summary) Record(l Line) {
	class := Classify(l.Operation)
	if class == Ignored {
		s.Ignored++
		return
	}
	app := AppOf(l)
	if app == "" {
		s.Unattributed++
		return
	}
	k := HourKey{App: app, Hour: HourOf(l.Time)}
	c := s.Hours[k]
	if c == nil {
		c = &Counts{}
		s.Hours[k] = c
	}
	switch {
	case l.Status >= 400 && l.Status < 500:
		c.Status4xx++
	case l.Status >= 500:
		c.Status5xx++
	}
	if l.Status == 401 || l.Status == 403 {
		c.Denied++
		return
	}
	switch class {
	case ClassA:
		c.ClassA++
	case ClassB:
		c.ClassB++
	case Delete:
		c.Deletes++
	}
	c.BytesOut += l.BytesSent
	if uploadOperations[l.Operation] && l.Status >= 200 && l.Status < 300 {
		c.BytesIn += l.ObjectSize
	}
}

// ParseLog reads one access-log object. Malformed lines (including lines
// longer than 64 KiB) are counted and skipped; only a read error fails.
func ParseLog(r io.Reader) (Summary, error) {
	s := Summary{Hours: map[HourKey]*Counts{}}
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, tooLong, err := readLine(br)
		if len(line) > 0 || tooLong {
			s.Lines++
			if tooLong {
				s.Malformed++
			} else if l, perr := ParseLine(string(line)); perr != nil {
				s.Malformed++
			} else {
				s.Record(l)
			}
		}
		if err == io.EOF {
			return s, nil
		}
		if err != nil {
			return Summary{}, fmt.Errorf("read access log: %w", err)
		}
	}
}

// readLine returns the next line without its newline; a line longer than
// maxLineBytes is discarded and reported as tooLong.
func readLine(br *bufio.Reader) (line []byte, tooLong bool, err error) {
	var buf bytes.Buffer
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong {
			buf.Write(chunk)
			if buf.Len() > maxLineBytes+1 {
				tooLong = true
				buf.Reset()
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if tooLong {
			return nil, true, err
		}
		return bytes.TrimRight(buf.Bytes(), "\n"), false, err
	}
}
