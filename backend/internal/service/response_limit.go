package service

import (
	"fmt"
	"io"
)

// A non-positive limit means "no limit" in both helpers below. They used to
// disagree — capReader treated 0 as unlimited while readAllWithLimit treated it
// as "every byte is too many" — so a limit that ever reached zero would either
// silently disable the cap or reject every response.
func readAllWithLimit(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	// Read at most limit+1 bytes so callers can detect oversized responses while
	// keeping memory use bounded by the configured response cap. A limit this
	// close to math.MaxInt64 cannot be incremented without wrapping negative
	// (io.LimitReader would then return EOF at once and the body would look
	// empty), so it is treated as unlimited.
	if limit == int64(^uint64(0)>>1) {
		return io.ReadAll(reader)
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return data, nil
}

// maxDrainBytes bounds how much of an unread response body closeBody discards.
// Draining lets net/http return an HTTP/1.1 connection to the idle pool instead
// of tearing it down; the cap keeps a huge or endless body from stalling us.
const maxDrainBytes = 64 << 10

// closeBody drains up to maxDrainBytes of body and then closes it. Use it
// instead of a bare Close whenever a decoder may stop before EOF (streaming
// JSON, size-limit errors), so keep-alive connections are reused.
func closeBody(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.CopyN(io.Discard, body, maxDrainBytes)
	_ = body.Close()
}

// capReader errors as soon as more than limit bytes have been pulled, so a
// streaming JSON decoder cannot buffer an unbounded AList payload.
//
// A body of exactly `limit` bytes is allowed: after the limit is reached the
// reader probes one extra byte to distinguish "the body ended exactly at the
// limit" (io.EOF) from "there is more" (error). Erroring on the first Read past
// the cap rejected legitimate responses that happened to match it.
type capReader struct {
	r     io.Reader
	n     int64
	limit int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.limit <= 0 {
		return c.r.Read(p)
	}
	remain := c.limit - c.n
	if remain <= 0 {
		var probe [1]byte
		n, err := c.r.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("response body exceeds %d bytes", c.limit)
		}
		return 0, err
	}
	// Read at most one byte past the remaining budget so the excess is detected
	// without buffering more than the cap. The comparison is written this way
	// because remain+1 would overflow for a limit near math.MaxInt64.
	if remain < int64(len(p)) {
		p = p[:remain+1]
	}
	n, err := c.r.Read(p)
	if int64(n) > remain {
		return 0, fmt.Errorf("response body exceeds %d bytes", c.limit)
	}
	c.n += int64(n)
	return n, err
}
