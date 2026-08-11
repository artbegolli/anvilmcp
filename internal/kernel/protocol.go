package kernel

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// maxFrameBytes guards against a corrupt or runaway length prefix. Individual
// output chunks are far smaller; a whole result frame (traceback + repr) is
// bounded well under this.
const maxFrameBytes = 16 << 20 // 16 MiB

// outExecute / outShutdown are the frames the server sends to the harness.
type outExecute struct {
	Type string `json:"type"` // "execute"
	ID   string `json:"id"`
	Code string `json:"code"`
}

type outShutdown struct {
	Type string `json:"type"` // "shutdown"
}

// inbound is every frame the harness can send. Fields are populated by type:
//
//	ready:  Python, Executable
//	stream: ID, Name, Text
//	result: ID, Status, ExecutionCount, ResultRepr, Ename, Evalue, Traceback
type inbound struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	Python     string `json:"python"`
	Executable string `json:"executable"`

	Name string `json:"name"`
	Text string `json:"text"`

	Status         string  `json:"status"`
	ExecutionCount int     `json:"execution_count"`
	ResultRepr     *string `json:"result_repr"`
	Ename          string  `json:"ename"`
	Evalue         string  `json:"evalue"`
	Traceback      string  `json:"traceback"`
}

// writeFrame writes a length-prefixed JSON frame.
func writeFrame(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > maxFrameBytes {
		return fmt.Errorf("outbound frame too large: %d bytes", len(data))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// readFrame reads one length-prefixed JSON frame. It honors any read deadline
// set on the underlying connection (returned as a net.Error timeout).
func readFrame(r io.Reader) (inbound, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return inbound{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrameBytes {
		return inbound{}, fmt.Errorf("inbound frame too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return inbound{}, err
	}
	var msg inbound
	if err := json.Unmarshal(buf, &msg); err != nil {
		return inbound{}, fmt.Errorf("decode frame: %w", err)
	}
	return msg, nil
}
