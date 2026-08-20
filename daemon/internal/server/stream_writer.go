package server

import (
	"context"
	"net/http"
	"time"
)

// streamWriteTimeout bounds one network write while still allowing an SSE
// response to remain idle indefinitely between writes.
const streamWriteTimeout = 60 * time.Second

// streamResponseWriter replaces http.Server's absolute response deadline with
// a deadline around each individual Write/Flush pair. A failed write cancels
// the handler context so its subscription and connection are released.
type streamResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	cancel     context.CancelFunc
	err        error
}

func newStreamResponseWriter(w http.ResponseWriter, cancel context.CancelFunc) (*streamResponseWriter, error) {
	stream := &streamResponseWriter{
		ResponseWriter: w,
		controller:     http.NewResponseController(w),
		cancel:         cancel,
	}
	// Validate deadline support and clear http.Server's absolute WriteTimeout.
	// Write and Flush install their own rolling deadline before touching the
	// connection, then Flush clears it again while the stream is idle.
	if err := stream.controller.SetWriteDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return stream, nil
}

func (w *streamResponseWriter) setWriteDeadline(deadline time.Time) error {
	if w.err != nil {
		return w.err
	}
	if err := w.controller.SetWriteDeadline(deadline); err != nil {
		return w.fail(err)
	}
	return nil
}

func (w *streamResponseWriter) fail(err error) error {
	if err != nil && w.err == nil {
		w.err = err
		w.cancel()
	}
	return err
}

func (w *streamResponseWriter) Write(data []byte) (int, error) {
	if err := w.setWriteDeadline(time.Now().Add(streamWriteTimeout)); err != nil {
		return 0, err
	}
	n, err := w.ResponseWriter.Write(data)
	return n, w.fail(err)
}

func (w *streamResponseWriter) Flush() {
	_ = w.FlushError()
}

func (w *streamResponseWriter) FlushError() error {
	if err := w.setWriteDeadline(time.Now().Add(streamWriteTimeout)); err != nil {
		return err
	}
	if err := w.controller.Flush(); err != nil {
		return w.fail(err)
	}
	return w.setWriteDeadline(time.Time{})
}
