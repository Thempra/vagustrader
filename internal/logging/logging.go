package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

type rotatingWriter struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	maxSize  int64
	maxFiles int
	written  int64
}

func newRotatingWriter(path string, maxSize int64, maxFiles int) (*rotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := &rotatingWriter{path: path, maxSize: maxSize, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.written = info.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.written+int64(len(p)) > w.maxSize {
		w.rotate()
	}
	n, err := w.file.Write(p)
	w.written += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() {
	w.file.Close()
	for i := w.maxFiles - 1; i > 0; i-- {
		old := fmt.Sprintf("%s.%d", w.path, i)
		next := fmt.Sprintf("%s.%d", w.path, i+1)
		os.Rename(old, next)
	}
	os.Rename(w.path, w.path+".1")
	w.open()
}

func Setup(logDir string) error {
	rw, err := newRotatingWriter(filepath.Join(logDir, "vagustrader.log"), 10*1024*1024, 5)
	if err != nil {
		return err
	}
	multi := io.MultiWriter(os.Stderr, rw)
	handler := slog.NewJSONHandler(multi, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	return nil
}
