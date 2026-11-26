package egress

import (
	"bytes"
	"crypto/x509"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthorityVouchesOnlyForItsHosts(t *testing.T) {
	a, err := newAuthority("agent-1", []string{"a.test", "b.test"})
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(a.cert)
	for host, ok := range map[string]bool{"a.test": true, "b.test": true, "other.test": false, "test": false} {
		// Signed by the authority's own key, so only the name constraint can
		// refuse it.
		c, err := a.leaf(host)
		require.NoError(t, err)
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		require.NoError(t, err)
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host})
		if ok {
			assert.NoError(t, err, host)
		} else {
			assert.Error(t, err, host)
		}
	}
}

func TestAuthorityNeedsAHost(t *testing.T) {
	_, err := newAuthority("agent-1", nil)
	assert.Error(t, err, "an authority with no name constraint would vouch for every host")
}

func TestScrubberCatchesAValueAtEveryCut(t *testing.T) {
	value := "secret-value-0123"
	pairs := []pair{{value: []byte(value), placeholder: []byte("PH")}}
	body := "before " + value + " middle " + value + value + " end " + value[:5]
	got, err := io.ReadAll(newScrubber(io.NopCloser(iotest.OneByteReader(strings.NewReader(body))), pairs))
	require.NoError(t, err)
	assert.Equal(t, "before PH middle PHPH end "+value[:5], string(got), "a prefix at the very end is passed on as it is")
	for cut := 1; cut < len(body); cut++ {
		r := io.MultiReader(strings.NewReader(body[:cut]), strings.NewReader(body[cut:]))
		got, err := io.ReadAll(newScrubber(io.NopCloser(r), pairs))
		require.NoError(t, err)
		assert.NotContains(t, string(got), value, "cut at %d", cut)
	}
}

func TestScrubberDoesNotHoldBackWhatCannotBeAValue(t *testing.T) {
	pairs := []pair{{value: []byte("secret-value"), placeholder: []byte("PH")}}
	pr, pw := io.Pipe()
	s := newScrubber(pr, pairs)
	go func() { _, _ = pw.Write([]byte("data: hello\n\n")) }()
	buf := make([]byte, 64)
	n, err := s.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "data: hello\n\n", string(buf[:n]), "a stream event goes out without waiting for more")
	_ = pw.Close()
}

func TestHeaderValue(t *testing.T) {
	v, err := headerValue([]byte("tok\n"))
	require.NoError(t, err)
	assert.Equal(t, "tok", string(v))
	for _, bad := range []string{"", " \n", "to\rk", "to\nk", "t\x00k"} {
		_, err := headerValue([]byte(bad))
		assert.Error(t, err, "%q", bad)
	}
	raw := []byte("tok\n")
	_, _ = headerValue(raw)
	assert.True(t, bytes.Equal(raw, make([]byte, 4)), "the resolved buffer is cleared")
}
