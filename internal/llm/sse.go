package llm

import (
	"bufio"
	"context"
	"io"
	"strings"
)

// SSEEvent represents a single Server-Sent Event per the SSE spec.
type SSEEvent struct {
	Event string `json:"event"`
	Data  string `json:"data"`
	ID    string `json:"id,omitempty"`
}

// sseReadLine reads one line delimited by \n, \r\n or \r, stripping the terminator.
// It returns the line without the delimiter. On EOF with pending data it returns that data.
func sseReadLine(r *bufio.Reader) (string, error) {
	var line []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF {
				if len(line) > 0 {
					return string(line), nil
				}
				return "", io.EOF
			}
			return "", err
		}
		if b == '\n' {
			return string(line), nil
		}
		if b == '\r' {
			// Peek for \n to consume CRLF as single terminator.
			peek, pErr := r.Peek(1)
			if pErr == nil && len(peek) > 0 && peek[0] == '\n' {
				_, _ = r.ReadByte()
			}
			return string(line), nil
		}
		line = append(line, b)
	}
}

// parseSSEStream reads SSE events from r and invokes handler for each dispatched event.
// It respects ctx cancellation and returns when r reaches EOF or ctx is cancelled.
// The function blocks until EOF/cancel. It implements the fragile surface documented
// as the v2 exit limitation: hand-rolled, zero-dependency, handles event:/data:,
// multi-line data concatenation, CRLF and LF, comment lines (: prefix), dispatch on blank line,
// truncated JSON (left to caller), unknown event types, missing data:.
func parseSSEStream(ctx context.Context, r io.Reader, handler func(SSEEvent) error) error {
	br := bufio.NewReader(r)
	var (
		currentEvent string
		currentData  []string
		currentID    string
	)

	dispatch := func() error {
		if len(currentData) == 0 {
			// Per SSE spec: do not dispatch empty data events (blank lines with no data).
			// This also makes "missing data:" gracefully produce no event.
			currentEvent = ""
			return nil
		}
		ev := SSEEvent{
			Event: currentEvent,
			Data:  strings.Join(currentData, "\n"),
			ID:    currentID,
		}
		// Reset for next event while preserving ID per spec; ID persists until overwritten.
		currentEvent = ""
		currentData = nil
		return handler(ev)
	}

	for {
		// Check context before blocking read.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := sseReadLine(br)
		if err != nil {
			if err == io.EOF {
				// Flush pending event if any data buffered (EOF without trailing blank line still dispatches).
				if len(currentData) > 0 {
					_ = dispatch()
				}
				return nil
			}
			return err
		}

		// Blank line -> dispatch.
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}

		// Comment line.
		if strings.HasPrefix(line, ":") {
			continue
		}

		var field, value string
		if idx := strings.Index(line, ":"); idx != -1 {
			field = line[:idx]
			value = line[idx+1:]
			// If value starts with single space, strip it.
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
		} else {
			field = line
			value = ""
		}

		switch field {
		case "event":
			currentEvent = value
		case "data":
			currentData = append(currentData, value)
		case "id":
			// Ignore IDs containing null character per spec.
			if !strings.Contains(value, "\x00") {
				currentID = value
			}
		case "retry":
			// Ignore retry field for LLM streaming; not relevant.
		default:
			// Unknown field: ignore gracefully.
		}
	}
}

// sseEventsToChannel is a helper for testing: consumes reader and returns channel of events.
// Not used by providers directly; providers use parseSSEStream with handler that emits StreamChunk.
func sseEventsToChannel(ctx context.Context, r io.Reader) (<-chan SSEEvent, <-chan error) {
	ch := make(chan SSEEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		err := parseSSEStream(ctx, r, func(ev SSEEvent) error {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
		if err != nil && err != io.EOF && err != context.Canceled {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	return ch, errCh
}
