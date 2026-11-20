package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// Wake is one reason to pull: a (re)connected realtime stream, or a
// nodes/<self> event carrying desired_seq.
type Wake struct {
	Connected bool
	Seq       int64
}

// Realtime keeps a server-sent event stream on /api/realtime subscribed to
// nodes/<self> and sends a Wake on every (re)connect and every event. It
// reconnects with jittered backoff from 1 to 30 seconds; the hosted server
// closes streams after 5 minutes, so reconnects are routine. Wakes are only
// hints: the loop always pulls the whole document.
type Realtime struct {
	client *Client
	logf   func(string, ...any)
	min    time.Duration
	max    time.Duration
}

// Run blocks until ctx ends.
func (r *Realtime) Run(ctx context.Context, wakes chan<- Wake) {
	backoff := r.min
	for ctx.Err() == nil {
		started := time.Now()
		err := r.stream(ctx, wakes)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = r.min // a stream that lasted was healthy
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		if err != nil {
			r.logf("realtime: %v; reconnecting in %s", err, wait.Round(100*time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, r.max)
	}
}

func (r *Realtime) stream(ctx context.Context, wakes chan<- Wake) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := r.client.newRequest(ctx, http.MethodGet, "/api/realtime", nil, "")
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := r.client.http.Do(req)
	if err != nil {
		return &Unreachable{Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode, Message: "realtime stream refused"}
	}
	topic := ""
	subscribed := false
	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	var event string
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("stream closed: %w", err)
		}
		if data.Len() > 1<<20 {
			return errors.New("realtime event too large")
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(line[len("data:"):]))
		case line == "":
			payload := data.String()
			name := event
			event = ""
			data.Reset()
			switch {
			case name == "PB_CONNECT":
				var hello struct {
					ClientID string `json:"clientId"`
				}
				if json.Unmarshal([]byte(payload), &hello) != nil || hello.ClientID == "" {
					return errors.New("realtime: no clientId in PB_CONNECT")
				}
				if err := r.client.Subscribe(ctx, hello.ClientID); err != nil {
					return fmt.Errorf("subscribe: %w", err)
				}
				topic = "nodes/" + r.client.NodeID()
				subscribed = true
				send(ctx, wakes, Wake{Connected: true})
			case subscribed && name == topic:
				var msg struct {
					Record struct {
						DesiredSeq int64 `json:"desired_seq"`
					} `json:"record"`
				}
				if json.Unmarshal([]byte(payload), &msg) == nil {
					send(ctx, wakes, Wake{Seq: msg.Record.DesiredSeq})
				}
			}
		}
	}
}

func send(ctx context.Context, wakes chan<- Wake, w Wake) {
	select {
	case wakes <- w:
	case <-ctx.Done():
	}
}
