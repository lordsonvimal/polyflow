package sidecar

import (
	"bytes"
	"encoding/binary"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"file":"a.templ","content_b64":"aGk="}`)
	require.NoError(t, WriteFrame(&buf, payload))

	got, err := ReadFrame(&buf)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestFrameRoundTripEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, nil))
	got, err := ReadFrame(&buf)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Pipes deliver partial reads; ReadFrame must reassemble a frame that
// arrives one byte at a time (io.ReadFull, not a single Read).
func TestReadFramePartialReads(t *testing.T) {
	var buf bytes.Buffer
	payload := bytes.Repeat([]byte("polyflow"), 100)
	require.NoError(t, WriteFrame(&buf, payload))

	got, err := ReadFrame(iotest.OneByteReader(&buf))
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

// A header announcing more than the cap is rejected without allocating.
func TestReadFrameOversizeHeaderRejected(t *testing.T) {
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], MaxFrameSize+1)
	_, err := ReadFrame(bytes.NewReader(header[:]))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds cap")
}

func TestWriteFrameOversizePayloadRejected(t *testing.T) {
	big := make([]byte, MaxFrameSize+1)
	err := WriteFrame(&bytes.Buffer{}, big)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds cap")
}

// Truncated payloads (dead sidecar mid-frame) surface as errors, not hangs.
func TestReadFrameTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteFrame(&buf, []byte("full payload")))
	truncated := buf.Bytes()[:buf.Len()-5]
	_, err := ReadFrame(bytes.NewReader(truncated))
	require.Error(t, err)
}
