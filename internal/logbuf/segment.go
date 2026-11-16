package logbuf

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Segment format, version 1 (all numbers big endian):
//
//	"EMSP" | version u8 | flags u8 | count u32 | token length u8 | token | gzip(payload)
//
// The header is plain so that a scan reads a few bytes per segment and never
// inflates anything. count is the number of records inside, the figure that
// keeps the pending count honest while the segment sits on disk. token is the
// idempotency token of the batch, the one every attempt to deliver it carries.
// flags bit 0 (segmentSent) says that an attempt to deliver the batch may have
// reached the storage already. The payload is opaque bytes to this file; the
// event buffer stores a JSON array, which tolerates new and removed fields and
// a changed array length, where a positional or schema-bound encoding would
// refuse every segment written before an upgrade.

const (
	segmentMagic   = "EMSP"
	segmentVersion = 1
	// segmentSent marks a segment whose batch may already be in the storage.
	segmentSent = 1
	// flagsOffset is the position of the flags byte in the header.
	flagsOffset = 5
	// maxTokenLen is the longest token the header can carry.
	maxTokenLen = 255
	// maxPayload bounds the inflated payload of a segment, so a damaged file
	// cannot be turned into an allocation of arbitrary size.
	maxPayload = 256 << 20
)

// errBadHeader marks a segment header that is not version 1: a wrong magic,
// another version, a cut-off or empty token.
var errBadHeader = errors.New("bad segment header")
// SegmentMeta is what a segment records besides its payload. Token is the
// idempotency token of the batch (at most 255 bytes). Count is the number of
// records in the payload. Sent is true when an attempt to deliver the batch may
// have reached the storage.
type SegmentMeta struct {
	Token string
	Count int
	Sent  bool
}

// Segment is a view of one segment in a queue. Name is the file name and the
// identity of the segment inside its queue; Size is the file size in bytes.
type Segment struct {
	SegmentMeta
	Name string
	Size int64
}

// encodeSegment builds the file body of version 1: the header and the gzip
// stream of payload. It returns an error for an empty or oversized token or a
// negative count.
func encodeSegment(meta SegmentMeta, payload []byte) ([]byte, error) {
	if len(meta.Token) == 0 || len(meta.Token) > maxTokenLen {
		return nil, fmt.Errorf("spool: the token must be 1 to %d bytes, got %d", maxTokenLen, len(meta.Token))
	}
	if meta.Count < 0 {
		return nil, errors.New("spool: negative record count")
	}
	var buf bytes.Buffer
	buf.WriteString(segmentMagic)
	var flags byte
	if meta.Sent {
		flags |= segmentSent
	}
	buf.WriteByte(segmentVersion)
	buf.WriteByte(flags)
	_ = binary.Write(&buf, binary.BigEndian, uint32(meta.Count))
	buf.WriteByte(byte(len(meta.Token)))
	buf.WriteString(meta.Token)
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// parseHeader reads the header of a segment from r. It returns an error for a
// wrong magic, an unknown version or a cut-off header.
//
// A header that cannot be understood wraps errBadHeader. A read that fails for
// another reason (the disk, not the file) is returned as it is, so that a caller
// never takes a transient I/O error for a damaged segment.
func parseHeader(r io.Reader) (SegmentMeta, error) {
	var fixed [4 + 1 + 1 + 4 + 1]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return SegmentMeta{}, fmt.Errorf("%w: short header: %v", errBadHeader, err)
		}
		return SegmentMeta{}, err
	}
	if string(fixed[:4]) != segmentMagic {
		return SegmentMeta{}, fmt.Errorf("%w: not a spool segment", errBadHeader)
	}
	if v := fixed[4]; v != segmentVersion {
		return SegmentMeta{}, fmt.Errorf("%w: unsupported segment version %d", errBadHeader, v)
	}
	meta := SegmentMeta{Sent: fixed[5]&segmentSent != 0, Count: int(binary.BigEndian.Uint32(fixed[6:10]))}
	tok := make([]byte, fixed[10])
	if _, err := io.ReadFull(r, tok); err != nil || len(tok) == 0 {
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return SegmentMeta{}, err
		}
		return SegmentMeta{}, fmt.Errorf("%w: segment token is missing", errBadHeader)
	}
	meta.Token = string(tok)
	return meta, nil
}

// readHeaderFile reads only the header of the segment at path.
func readHeaderFile(path string) (SegmentMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return SegmentMeta{}, err
	}
	defer f.Close()
	return parseHeader(f)
}

// decodeSegment parses a whole segment and returns its header and inflated
// payload. It returns an error for a bad header, a damaged gzip stream or a
// payload above maxPayload.
func decodeSegment(data []byte) (SegmentMeta, []byte, error) {
	r := bytes.NewReader(data)
	meta, err := parseHeader(r)
	if err != nil {
		return SegmentMeta{}, nil, err
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		return SegmentMeta{}, nil, fmt.Errorf("damaged body: %w", err)
	}
	payload, err := io.ReadAll(io.LimitReader(zr, maxPayload+1))
	if err != nil {
		return SegmentMeta{}, nil, fmt.Errorf("damaged body: %w", err)
	}
	if len(payload) > maxPayload {
		return SegmentMeta{}, nil, errors.New("payload exceeds the limit")
	}
	return meta, payload, nil
}
