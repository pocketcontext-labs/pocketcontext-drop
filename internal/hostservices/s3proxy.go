// Package hostservices is the host side of a guest's host services (vsock
// 51007, docs/agent.md "Host services"): `drop agent host-services` accepts
// connections that nibrunnerd forwards from a running guest, one header line
// naming the app and deployment, and serves an S3 proxy on them. The guest
// holds no AWS credential: it signs with static dummy keys, the proxy ignores
// that signature, confines every request to the app's own `<appId>/` prefix
// and the operations Crew's clients use, enforces the app's limits and signs
// again with the app's own short-lived STS session.
package hostservices

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Payload hash values of x-amz-content-sha256.
const (
	unsignedPayload          = "UNSIGNED-PAYLOAD"
	streamingUnsignedTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	streamingSigned          = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	emptySHA256              = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// maxXMLBody bounds the request bodies the proxy reads itself (DeleteObjects,
// CompleteMultipartUpload).
const maxXMLBody = 1 << 20

// Op is one S3 operation the proxy allows.
type Op string

const (
	OpGetObject      Op = "GetObject"
	OpHeadObject     Op = "HeadObject"
	OpPutObject      Op = "PutObject"
	OpDeleteObject   Op = "DeleteObject"
	OpDeleteObjects  Op = "DeleteObjects"
	OpListObjectsV2  Op = "ListObjectsV2"
	OpCreateUpload   Op = "CreateMultipartUpload"
	OpUploadPart     Op = "UploadPart"
	OpCompleteUpload Op = "CompleteMultipartUpload"
	OpAbortUpload    Op = "AbortMultipartUpload"
)

// Class is the usage class of an operation.
func (o Op) Class() string {
	switch o {
	case OpGetObject, OpHeadObject:
		return ClassRead
	case OpListObjectsV2:
		return ClassList
	case OpDeleteObject, OpDeleteObjects, OpAbortUpload:
		return ClassDelete
	}
	return ClassWrite
}

// s3Request is one parsed, allowed guest request.
type s3Request struct {
	op     Op
	key    string // object key (empty for bucket operations)
	query  [][2]string
	size   int64 // payload bytes for PutObject and UploadPart
	upload string
	part   int
}

// s3Error is an S3-style refusal.
type s3Error struct {
	status  int
	code    string
	message string
}

func (e *s3Error) Error() string { return e.code + ": " + e.message }

func refuse(status int, code, message string) *s3Error {
	return &s3Error{status: status, code: code, message: message}
}

func accessDenied(message string) *s3Error {
	return refuse(http.StatusForbidden, "AccessDenied", message)
}

// writeError writes an S3 XML error document.
func writeError(w http.ResponseWriter, e *s3Error) {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString("<Error><Code>")
	_ = xml.EscapeText(&b, []byte(e.code))
	b.WriteString("</Code><Message>")
	_ = xml.EscapeText(&b, []byte(e.message))
	b.WriteString("</Message></Error>")
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(b.Len()))
	if e.status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(e.status)
	_, _ = w.Write(b.Bytes())
}

// query parameters each operation may carry; x-id is the SDK's own marker.
var (
	objectParams = map[string]bool{"x-id": true}
	partParams   = map[string]bool{"x-id": true, "partNumber": true, "uploadId": true}
	listParams   = map[string]bool{"x-id": true, "list-type": true, "prefix": true, "delimiter": true, "max-keys": true,
		"continuation-token": true, "start-after": true, "encoding-type": true, "fetch-owner": true}
)

// checkKey accepts a key under the app's prefix: valid UTF-8, at most 1024
// bytes, no control characters and no empty, "." or ".." segment, so no
// layer between here and S3 can read it as another key.
func checkKey(key, prefix string) *s3Error {
	if !strings.HasPrefix(key, prefix) || len(key) > 1024 || !utf8.ValidString(key) {
		return accessDenied("keys must start with the app's prefix")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return accessDenied("keys must not hold control characters")
		}
	}
	for _, seg := range strings.Split(strings.TrimSuffix(key, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return accessDenied("keys must not hold empty, . or .. segments")
		}
	}
	return nil
}

// checkPrefixParam accepts a list prefix or start-after under the app's prefix.
func checkPrefixParam(value, prefix string) *s3Error {
	if !strings.HasPrefix(value, prefix) || len(value) > 1024 || !utf8.ValidString(value) {
		return accessDenied("listings must stay under the app's prefix")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return accessDenied("listing parameters must not hold control characters")
		}
	}
	return nil
}

// parseRequest classifies a path-style guest request and checks it against
// the app's bucket and prefix. Anything not listed is refused.
func parseRequest(r *http.Request, bucket, prefix string) (*s3Request, *s3Error) {
	raw := r.URL.EscapedPath()
	if !strings.HasPrefix(raw, "/") {
		return nil, refuse(http.StatusBadRequest, "InvalidURI", "path-style requests only")
	}
	path, err := url.PathUnescape(raw[1:])
	if err != nil {
		return nil, refuse(http.StatusBadRequest, "InvalidURI", "invalid path encoding")
	}
	b, key, _ := strings.Cut(path, "/")
	if b != bucket {
		return nil, accessDenied("only the app's replica bucket is available")
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, refuse(http.StatusBadRequest, "InvalidArgument", "invalid query")
	}
	req := &s3Request{key: key}
	has := func(name string) bool { _, ok := values[name]; return ok }
	allow := func(allowed map[string]bool) *s3Error {
		for name, v := range values {
			if !allowed[name] || len(v) != 1 {
				return refuse(http.StatusNotImplemented, "NotImplemented", "this operation is not available through Drop host services")
			}
		}
		for name, v := range values {
			req.query = append(req.query, [2]string{name, v[0]})
		}
		return nil
	}
	if r.Header.Get("X-Amz-Copy-Source") != "" {
		return nil, refuse(http.StatusNotImplemented, "NotImplemented", "copy operations are not available through Drop host services")
	}
	if key == "" {
		switch {
		case r.Method == http.MethodGet && values.Get("list-type") == "2":
			if e := allow(listParams); e != nil {
				return nil, e
			}
			if e := checkPrefixParam(values.Get("prefix"), prefix); e != nil {
				return nil, e
			}
			if has("start-after") {
				if e := checkPrefixParam(values.Get("start-after"), prefix); e != nil {
					return nil, e
				}
			}
			req.op = OpListObjectsV2
			return req, nil
		case r.Method == http.MethodPost && has("delete"):
			if len(values) > 2 || (len(values) == 2 && !has("x-id")) || values.Get("delete") != "" {
				return nil, refuse(http.StatusNotImplemented, "NotImplemented", "this operation is not available through Drop host services")
			}
			req.query = [][2]string{{"delete", ""}}
			req.op = OpDeleteObjects
			return req, nil
		}
		return nil, refuse(http.StatusNotImplemented, "NotImplemented", "this bucket operation is not available through Drop host services")
	}
	if e := checkKey(key, prefix); e != nil {
		return nil, e
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if e := allow(objectParams); e != nil {
			return nil, e
		}
		req.op = OpGetObject
		if r.Method == http.MethodHead {
			req.op = OpHeadObject
		}
	case http.MethodPut:
		if has("uploadId") || has("partNumber") {
			if e := allow(partParams); e != nil {
				return nil, e
			}
			part, err := strconv.Atoi(values.Get("partNumber"))
			if err != nil || part < 1 || part > 10000 || values.Get("uploadId") == "" || len(values.Get("uploadId")) > 1024 {
				return nil, refuse(http.StatusBadRequest, "InvalidArgument", "UploadPart needs partNumber and uploadId")
			}
			req.op, req.part, req.upload = OpUploadPart, part, values.Get("uploadId")
		} else {
			if e := allow(objectParams); e != nil {
				return nil, e
			}
			req.op = OpPutObject
		}
	case http.MethodPost:
		switch {
		case has("uploads") && values.Get("uploads") == "" && !has("uploadId"):
			delete(values, "uploads")
			if e := allow(objectParams); e != nil {
				return nil, e
			}
			req.query = append(req.query, [2]string{"uploads", ""})
			req.op = OpCreateUpload
		case has("uploadId") && !has("partNumber"):
			if e := allow(partParams); e != nil {
				return nil, e
			}
			req.op, req.upload = OpCompleteUpload, values.Get("uploadId")
		default:
			return nil, refuse(http.StatusNotImplemented, "NotImplemented", "this operation is not available through Drop host services")
		}
	case http.MethodDelete:
		if has("uploadId") {
			if e := allow(partParams); e != nil {
				return nil, e
			}
			req.op, req.upload = OpAbortUpload, values.Get("uploadId")
		} else {
			if e := allow(objectParams); e != nil {
				return nil, e
			}
			req.op = OpDeleteObject
		}
	default:
		return nil, refuse(http.StatusMethodNotAllowed, "MethodNotAllowed", "method not allowed")
	}
	return req, nil
}

// forwardedHeaders are the request headers that reach S3. Everything else
// (Authorization, the guest's date and token, ACLs, grants, storage class,
// tagging, object lock, encryption choices, redirects) is dropped, so the
// bucket's own defaults apply.
var forwardedHeaders = map[string]bool{
	"Content-Type": true, "Content-Md5": true, "Content-Disposition": true, "Content-Language": true,
	"Cache-Control": true, "Expires": true, "If-Match": true, "If-None-Match": true, "If-Modified-Since": true,
	"If-Unmodified-Since": true, "Range": true, "X-Amz-Sdk-Checksum-Algorithm": true, "X-Amz-Checksum-Mode": true,
	"X-Amz-Trailer": true, "X-Amz-Decoded-Content-Length": true, "X-Amz-Checksum-Type": true,
}

func forwardHeader(name string) bool {
	return forwardedHeaders[name] || strings.HasPrefix(name, "X-Amz-Meta-") || strings.HasPrefix(name, "X-Amz-Checksum-")
}

// s3Escape encodes a path the way S3 canonicalizes it: everything but
// unreserved characters and the separator.
func s3Escape(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' || (keepSlash && c == '/') {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// payload describes how the guest's body travels upstream.
type payload struct {
	hash    string    // x-amz-content-sha256 sent upstream
	body    io.Reader // what is sent
	length  int64     // Content-Length sent upstream (-1 none)
	decoded int64     // object bytes the request carries
	chunked bool      // aws-chunked framing kept (unsigned trailer)
}

// preparePayload maps the guest's payload mode. A hex hash or
// UNSIGNED-PAYLOAD passes through (S3 verifies a hex hash against the bytes).
// The unsigned trailer form passes through framed. The signed streaming form
// carries chunk signatures chained from the guest's dummy key: the proxy
// strips the framing and sends the bytes unsigned. Its trailer variant is
// refused.
func preparePayload(r *http.Request) (*payload, *s3Error) {
	mode := r.Header.Get("X-Amz-Content-Sha256")
	if r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody {
		return nil, refuse(http.StatusLengthRequired, "MissingContentLength", "requests with a body need Content-Length")
	}
	length := max(r.ContentLength, 0)
	p := &payload{hash: mode, body: r.Body, length: length, decoded: length}
	switch {
	case mode == "":
		p.hash = unsignedPayload
		if length == 0 {
			p.hash = emptySHA256
		}
	case len(mode) == 64 && isHex(mode), mode == unsignedPayload:
	case mode == streamingUnsignedTrailer:
		decoded, err := strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64)
		if err != nil || decoded < 0 {
			return nil, refuse(http.StatusBadRequest, "InvalidArgument", "aws-chunked requests need x-amz-decoded-content-length")
		}
		p.decoded, p.chunked = decoded, true
	case mode == streamingSigned:
		decoded, err := strconv.ParseInt(r.Header.Get("X-Amz-Decoded-Content-Length"), 10, 64)
		if err != nil || decoded < 0 {
			return nil, refuse(http.StatusBadRequest, "InvalidArgument", "aws-chunked requests need x-amz-decoded-content-length")
		}
		p.hash, p.decoded, p.length = unsignedPayload, decoded, decoded
		p.body = &exactReader{r: newChunkDecoder(r.Body), n: decoded}
	default:
		return nil, refuse(http.StatusNotImplemented, "NotImplemented", "this payload signing mode is not available through Drop host services")
	}
	return p, nil
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// chunkDecoder reads the aws-chunked framing of a signed streaming body
// (`<hex size>;chunk-signature=<sig>\r\n<data>\r\n`, ending with a zero
// chunk) and yields the data. Signatures are ignored: the guest's key is a
// dummy one.
type chunkDecoder struct {
	r    *bufio.Reader
	left int64
	done bool
	err  error
}

func newChunkDecoder(r io.Reader) *chunkDecoder { return &chunkDecoder{r: bufio.NewReader(r)} }

func (c *chunkDecoder) Read(p []byte) (int, error) {
	for c.left == 0 {
		if c.err != nil {
			return 0, c.err
		}
		if c.done {
			return 0, io.EOF
		}
		line, err := c.r.ReadString('\n')
		if err != nil || len(line) > 4096 || !strings.HasSuffix(line, "\r\n") {
			c.err = errors.New("invalid aws-chunked framing")
			return 0, c.err
		}
		sizeHex, _, _ := strings.Cut(strings.TrimSuffix(line, "\r\n"), ";")
		size, err := strconv.ParseInt(sizeHex, 16, 64)
		if err != nil || size < 0 {
			c.err = errors.New("invalid aws-chunked size")
			return 0, c.err
		}
		if size == 0 {
			c.done = true
			continue
		}
		c.left = size
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left == 0 && err == nil {
		var crlf [2]byte
		if _, err := io.ReadFull(c.r, crlf[:]); err != nil || crlf != [2]byte{'\r', '\n'} {
			c.err = errors.New("invalid aws-chunked framing")
		}
	}
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

// exactReader yields exactly n bytes or fails.
type exactReader struct {
	r io.Reader
	n int64
}

func (e *exactReader) Read(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > e.n {
		p = p[:e.n]
	}
	n, err := e.r.Read(p)
	e.n -= int64(n)
	if err == io.EOF && e.n > 0 {
		err = io.ErrUnexpectedEOF
	}
	if e.n == 0 && err == io.EOF {
		err = nil
	}
	return n, err
}

// countingReader counts what the upstream request read from the guest.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Upstream is where the proxy sends signed requests.
type Upstream struct {
	// Endpoint overrides https://s3.<region>.amazonaws.com (tests and S3
	// compatible stores; path-style).
	Endpoint string
	Client   *http.Client
}

// s3Signer signs for S3: the path is already escaped once.
var s3Signer = v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })

func (u *Upstream) base(region string) (*url.URL, error) {
	if u.Endpoint != "" {
		return url.Parse(u.Endpoint)
	}
	return url.Parse("https://s3." + region + ".amazonaws.com")
}

// build makes the signed upstream request for req.
func (u *Upstream) build(ctx context.Context, in *http.Request, req *s3Request, p *payload, bucket, region string, creds aws.Credentials, now time.Time) (*http.Request, error) {
	base, err := u.base(region)
	if err != nil {
		return nil, err
	}
	path := "/" + bucket
	if req.key != "" {
		path += "/" + req.key
	}
	var q []string
	for _, kv := range req.query {
		if kv[1] == "" && (kv[0] == "uploads" || kv[0] == "delete") {
			q = append(q, s3Escape(kv[0], false))
			continue
		}
		q = append(q, s3Escape(kv[0], false)+"="+s3Escape(kv[1], false))
	}
	target := *base
	target.Path = strings.TrimSuffix(base.Path, "/") + path
	target.RawPath = strings.TrimSuffix(base.EscapedPath(), "/") + s3Escape(path, true)
	target.RawQuery = strings.Join(q, "&")
	var body io.Reader
	if p.length > 0 {
		body = p.body
	}
	out, err := http.NewRequestWithContext(ctx, in.Method, target.String(), body)
	if err != nil {
		return nil, err
	}
	out.URL.RawPath = target.RawPath
	out.ContentLength = p.length
	if p.length == 0 {
		out.Body = http.NoBody
	}
	for name, values := range in.Header {
		if forwardHeader(name) {
			out.Header[name] = append([]string(nil), values...)
		}
	}
	if p.chunked {
		out.Header.Set("Content-Encoding", "aws-chunked")
	} else {
		out.Header.Del("X-Amz-Decoded-Content-Length")
		out.Header.Del("X-Amz-Trailer")
	}
	out.Header.Set("X-Amz-Content-Sha256", p.hash)
	if p.length > 0 {
		// S3 answers a failed precondition before reading the body; without
		// 100-continue that answer races the upload and can surface as a reset.
		out.Header.Set("Expect", "100-continue")
	}
	if err := s3Signer.SignHTTP(ctx, creds, out, p.hash, "s3", region, now); err != nil {
		return nil, err
	}
	return out, nil
}

// deleteList is the body of DeleteObjects.
type deleteList struct {
	XMLName xml.Name `xml:"Delete"`
	Objects []struct {
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId"`
	} `xml:"Object"`
}

// readDeleteKeys reads and checks a DeleteObjects body: at most 1000 keys,
// each under the prefix, never a version (that would delete permanently).
func readDeleteKeys(body io.Reader, prefix string) ([]byte, []string, *s3Error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxXMLBody+1))
	if err != nil || len(raw) > maxXMLBody {
		return nil, nil, refuse(http.StatusBadRequest, "MalformedXML", "the delete list is too large")
	}
	var d deleteList
	if err := xml.Unmarshal(raw, &d); err != nil || len(d.Objects) == 0 || len(d.Objects) > 1000 {
		return nil, nil, refuse(http.StatusBadRequest, "MalformedXML", "invalid delete list")
	}
	keys := make([]string, 0, len(d.Objects))
	for _, o := range d.Objects {
		if o.VersionID != "" {
			return nil, nil, accessDenied("version deletes are not available through Drop host services")
		}
		if e := checkKey(o.Key, prefix); e != nil {
			return nil, nil, e
		}
		keys = append(keys, o.Key)
	}
	return raw, keys, nil
}
