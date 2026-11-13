package main

import (
	"io"
	"sync"
)

// pipe is an io.Reader fed one line at a time by the test.
type pipe struct {
	r  *io.PipeReader
	w  *io.PipeWriter
	mu sync.Mutex
}

func newPipe() *pipe {
	r, w := io.Pipe()
	return &pipe{r: r, w: w}
}

func (p *pipe) Read(b []byte) (int, error) { return p.r.Read(b) }

func (p *pipe) writeLine(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.w.Write([]byte(s + "\n"))
}

func (p *pipe) close() { _ = p.w.Close() }
