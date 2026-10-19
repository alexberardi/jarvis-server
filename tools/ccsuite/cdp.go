package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// cdpPipe is the Chrome DevTools Protocol over --remote-debugging-pipe: JSON messages, each
// ended by a NUL byte, written to the browser's fd 3 and read from its fd 4. Only browser-level
// commands (no target sessions); events are dropped.
type cdpPipe struct {
	w       io.WriteCloser
	mu      sync.Mutex
	next    int
	waiting map[int]chan cdpReply
	closed  chan struct{}
}

type cdpReply struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func newCDP(w io.WriteCloser, r io.ReadCloser) *cdpPipe {
	c := &cdpPipe{w: w, waiting: map[int]chan cdpReply{}, closed: make(chan struct{})}
	go func() {
		defer close(c.closed)
		defer r.Close()
		br := bufio.NewReader(r)
		for {
			msg, err := br.ReadBytes(0)
			if err != nil {
				return
			}
			var rep cdpReply
			if json.Unmarshal(msg[:len(msg)-1], &rep) != nil || rep.ID == 0 {
				continue // an event
			}
			c.mu.Lock()
			ch := c.waiting[rep.ID]
			delete(c.waiting, rep.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- rep
			}
		}
	}()
	return c
}

// Call sends one command and waits for its answer.
func (c *cdpPipe) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan cdpReply, 1)
	c.waiting[id] = ch
	c.mu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	b, _ := json.Marshal(msg)
	if _, err := c.w.Write(append(b, 0)); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	select {
	case rep := <-ch:
		if rep.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, rep.Error.Message)
		}
		return rep.Result, nil
	case <-c.closed:
		return nil, fmt.Errorf("%s: %w", method, errors.New("the browser closed its DevTools pipe"))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *cdpPipe) Close() { c.w.Close() }
