// Package loglimit bounds retained output without interrupting its producer.
package loglimit

import (
	"fmt"
	"io"
	"strconv"

	"spikeloader/internal/env"
)

const DefaultBytes int64 = 50 << 20
const MaxBytes int64 = 1024 << 20

func Config() (int64, error) {
	value := env.Get("CI_MAX_LOG_MB", "50")
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 || n > 1024 {
		return 0, fmt.Errorf("CI_MAX_LOG_MB must be an integer between 1 and 1024 (got %q)", value)
	}
	return n << 20, nil
}

func Notice(limit int64) string { return fmt.Sprintf("\n… log truncated at %d MB\n", limit>>20) }

type Writer struct {
	Out            io.Writer
	Limit, Written int64
	Truncated      bool
}

func (w *Writer) Write(b []byte) (int, error) {
	if w.Truncated {
		return len(b), nil
	}
	notice := []byte(Notice(w.Limit))
	room := max(w.Limit-int64(len(notice))-w.Written, 0)
	n := min(int64(len(b)), room)
	if n > 0 {
		written, err := w.Out.Write(b[:n])
		w.Written += int64(written)
		if err != nil {
			return written, err
		}
		if int64(written) != n {
			return written, io.ErrShortWrite
		}
	}
	if int64(len(b)) > room {
		// Existing logs may already fill the configured budget (e.g. after reducing it).
		remaining := max(w.Limit-w.Written, 0)
		toWrite := notice[:min(int64(len(notice)), remaining)]
		n, err := w.Out.Write(toWrite)
		w.Written += int64(n)
		if err != nil {
			return n, err
		}
		if n != len(toWrite) {
			return n, io.ErrShortWrite
		}
		w.Truncated = true
	}
	return len(b), nil
}
