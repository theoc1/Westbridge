package ami

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// DefaultMaxPacketSize caps how many bytes a single packet may occupy. Asterisk
// packets are a few hundred bytes; the cap only exists so that a broken or
// hostile peer cannot make the reader allocate without bound.
const DefaultMaxPacketSize = 1 << 20

// bannerPrefix is the greeting Asterisk sends immediately after a manager
// connection is accepted, e.g. "Asterisk Call Manager/2.10.6".
const bannerPrefix = "Asterisk Call Manager/"

// ErrPacketTooLarge is returned when a packet exceeds the decoder's size cap.
// The connection cannot be resynchronised after this, so callers should treat
// it as fatal and reconnect.
var ErrPacketTooLarge = errors.New("ami: packet exceeds maximum size")

// ErrNotAsterisk is returned by ReadBanner when the greeting does not look like
// an AMI banner, which usually means the address points at some other service.
var ErrNotAsterisk = errors.New("ami: peer did not send an Asterisk Call Manager banner")

// Decoder reads AMI packets from a stream. It is not safe for concurrent use:
// exactly one goroutine owns the connection and drives the decoder.
type Decoder struct {
	r *bufio.Reader

	// MaxPacketSize caps a single packet. Zero means DefaultMaxPacketSize.
	MaxPacketSize int
}

// NewDecoder returns a Decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: bufio.NewReader(r)}
}

// ReadBanner consumes the greeting line that precedes every AMI session. It is
// a bare line with no terminating blank line, so it must be read before the
// first Decode call rather than through it.
func (d *Decoder) ReadBanner() (string, error) {
	line, err := d.readLine(d.limit())
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(line, bannerPrefix) {
		return line, ErrNotAsterisk
	}
	return line, nil
}

// Decode reads exactly one packet. Blank lines between packets are tolerated
// and skipped. It returns io.EOF when the stream ends cleanly on a packet
// boundary, and io.ErrUnexpectedEOF when it ends part-way through a packet —
// a partial packet is never returned as if it were complete.
func (d *Decoder) Decode() (*Message, error) {
	msg := &Message{}
	budget := d.limit()

	for {
		line, err := d.readLine(budget)
		if err != nil {
			if errors.Is(err, io.EOF) && len(msg.Fields) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}

		budget -= len(line) + 2 // the CRLF is part of the packet's size
		if budget < 0 {
			return nil, ErrPacketTooLarge
		}

		if line == "" {
			if len(msg.Fields) == 0 {
				continue // stray separator, not an empty packet
			}
			return msg, nil
		}

		key, value, found := strings.Cut(line, ":")
		if key = strings.TrimSpace(key); !found || key == "" {
			// A line without a usable key, such as the "--END COMMAND--"
			// terminator of a "Response: Follows" packet.
			msg.Add("", line)
			continue
		}
		msg.Add(key, strings.TrimSpace(value))
	}
}

func (d *Decoder) limit() int {
	if d.MaxPacketSize > 0 {
		return d.MaxPacketSize
	}
	return DefaultMaxPacketSize
}

// readLine returns the next line without its CRLF or LF terminator. Asterisk
// always sends CRLF, but a bare LF is accepted so that hand-written fixtures
// and proxies do not have to be byte-perfect.
func (d *Decoder) readLine(budget int) (string, error) {
	var buf []byte
	for {
		frag, err := d.r.ReadSlice('\n')
		if len(buf)+len(frag) > budget {
			return "", ErrPacketTooLarge
		}
		buf = append(buf, frag...)

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				// A final line with no terminator means the peer went away
				// mid-packet; Decode turns this into io.ErrUnexpectedEOF.
				return "", io.ErrUnexpectedEOF
			}
			return "", err
		}
		return strings.TrimRight(string(buf), "\r\n"), nil
	}
}
