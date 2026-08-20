package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type streamWriterStub struct {
	header           http.Header
	deadlines        []time.Time
	setDeadlineErrAt int
	writeErr         error
	flushErr         error
	written          []byte
	flushes          int
}

func (w *streamWriterStub) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*streamWriterStub) WriteHeader(int) {}

func (w *streamWriterStub) Write(data []byte) (int, error) {
	w.written = append(w.written, data...)
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(data), nil
}

func (w *streamWriterStub) FlushError() error {
	w.flushes++
	return w.flushErr
}

func (w *streamWriterStub) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	if w.setDeadlineErrAt == len(w.deadlines) {
		return errors.New("set deadline failed")
	}
	return nil
}

func TestStreamResponseWriterBoundsEachWriteAndClearsIdleDeadline(t *testing.T) {
	underlying := &streamWriterStub{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := newStreamResponseWriter(underlying, cancel)
	if err != nil {
		t.Fatalf("new stream writer: %v", err)
	}

	if _, err := stream.Write([]byte("event")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := stream.FlushError(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if got := string(underlying.written); got != "event" {
		t.Fatalf("written = %q, want event", got)
	}
	if underlying.flushes != 1 {
		t.Fatalf("flushes = %d, want 1", underlying.flushes)
	}
	if len(underlying.deadlines) != 4 {
		t.Fatalf("deadline updates = %d, want 4", len(underlying.deadlines))
	}
	if !underlying.deadlines[0].IsZero() || underlying.deadlines[1].IsZero() ||
		underlying.deadlines[2].IsZero() || !underlying.deadlines[3].IsZero() {
		t.Fatalf("unexpected deadline sequence: %v", underlying.deadlines)
	}
	select {
	case <-ctx.Done():
		t.Fatal("successful stream write canceled the handler")
	default:
	}
}

func TestStreamResponseWriterCancelsHandlerOnFailures(t *testing.T) {
	tests := []struct {
		name             string
		setDeadlineErrAt int
		writeErr         error
		flushErr         error
		operation        func(*streamResponseWriter) error
	}{
		{
			name:             "write deadline",
			setDeadlineErrAt: 2,
			operation: func(stream *streamResponseWriter) error {
				_, err := stream.Write([]byte("event"))
				return err
			},
		},
		{
			name:     "write",
			writeErr: errors.New("write failed"),
			operation: func(stream *streamResponseWriter) error {
				_, err := stream.Write([]byte("event"))
				return err
			},
		},
		{
			name:     "flush",
			flushErr: errors.New("flush failed"),
			operation: func(stream *streamResponseWriter) error {
				if _, err := stream.Write([]byte("event")); err != nil {
					return err
				}
				return stream.FlushError()
			},
		},
		{
			name:             "clear idle deadline",
			setDeadlineErrAt: 4,
			operation: func(stream *streamResponseWriter) error {
				if _, err := stream.Write([]byte("event")); err != nil {
					return err
				}
				return stream.FlushError()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			underlying := &streamWriterStub{
				setDeadlineErrAt: tt.setDeadlineErrAt,
				writeErr:         tt.writeErr,
				flushErr:         tt.flushErr,
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := newStreamResponseWriter(underlying, cancel)
			if err != nil {
				t.Fatalf("new stream writer: %v", err)
			}

			if err := tt.operation(stream); err == nil {
				t.Fatal("operation succeeded, want error")
			}
			select {
			case <-ctx.Done():
			default:
				t.Fatal("stream failure did not cancel the handler")
			}

			// Once failed, the wrapper returns the original error without
			// touching the connection again.
			deadlineCount := len(underlying.deadlines)
			stream.Flush()
			if len(underlying.deadlines) != deadlineCount {
				t.Fatal("failed stream attempted another deadline update")
			}
		})
	}
}
