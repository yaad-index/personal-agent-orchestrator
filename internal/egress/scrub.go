package egress

import (
	"bytes"
	"io"
)

// scrubber reads a response body with every secret value in it put back to its
// placeholder. It holds back the last few bytes of what it has read until more
// arrives, so a value split across two reads is still caught.
type scrubber struct {
	src   io.ReadCloser
	pairs []pair
	in    []byte
	out   []byte
	eof   bool
	// atEOF runs once the body is read to its end, before the end is
	// returned: the moment a response's trailers are known.
	atEOF func()
}

type pair struct{ value, placeholder []byte }

func newScrubber(src io.ReadCloser, pairs []pair) *scrubber {
	return &scrubber{src: src, pairs: pairs}
}

func (s *scrubber) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.eof {
			if len(s.in) == 0 {
				if s.atEOF != nil {
					s.atEOF()
					s.atEOF = nil
				}
				return 0, io.EOF
			}
			s.out, s.in = s.in, nil
			break
		}
		buf := make([]byte, 32<<10)
		n, err := s.src.Read(buf)
		s.in = s.replace(append(s.in, buf[:n]...))
		switch {
		case err == io.EOF:
			s.eof = true
		case err != nil:
			return 0, err
		}
		// Every whole value is replaced by now, so only a tail that could be
		// the start of a value is held back for the next read; the rest goes
		// out at once, and a stream does not stall.
		if s.eof {
			continue
		}
		if cut := len(s.in) - s.held(s.in); cut > 0 {
			s.out = append([]byte(nil), s.in[:cut]...)
			s.in = append([]byte(nil), s.in[cut:]...)
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

// held is the length of the longest tail of b that is a proper prefix of a
// value.
func (s *scrubber) held(b []byte) int {
	n := 0
	for _, p := range s.pairs {
		for k := min(len(p.value)-1, len(b)); k > n; k-- {
			if bytes.HasSuffix(b, p.value[:k]) {
				n = k
				break
			}
		}
	}
	return n
}

func (s *scrubber) replace(b []byte) []byte {
	for _, p := range s.pairs {
		if bytes.Contains(b, p.value) {
			b = bytes.ReplaceAll(b, p.value, p.placeholder)
		}
	}
	return b
}

func (s *scrubber) Close() error { return s.src.Close() }
