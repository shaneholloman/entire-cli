package githelper

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"

	"github.com/entireio/cli/internal/remotehelper/debuglog"
	"github.com/entireio/cli/internal/remotehelper/gitproto"
)

// handleConnect handles a connect session for the given service.
func handleConnect(ctx context.Context, t Transport, service string, stdin io.Reader, stdout io.Writer) error {
	refs, err := t.InfoRefs(ctx, service)
	if err != nil {
		return fmt.Errorf("connect %s info/refs: %w", service, err)
	}
	defer refs.Close()

	refReader := bufio.NewReader(refs)
	var reply packp.SmartReply
	if err := reply.Decode(refReader); err != nil {
		return fmt.Errorf("parsing info/refs: %w", err)
	}

	if _, err := io.Copy(stdout, refReader); err != nil {
		return fmt.Errorf("streaming refs: %w", err)
	}

	switch service {
	case "git-upload-pack":
		return handleFetch(ctx, t, stdin, stdout)
	case "git-receive-pack":
		// fall through
	default:
		return fmt.Errorf("unsupported service: %s", service)
	}

	reqBody, err := gitproto.ReadReceivePackRequest(stdin)
	if err != nil {
		return fmt.Errorf("reading git request: %w", err)
	}
	reqBody, err = gitproto.AppendAgentToReceivePackRequest(reqBody, Agent)
	if err != nil {
		return fmt.Errorf("amending receive-pack agent: %w", err)
	}
	debuglog.Printf("git request: %d bytes", len(reqBody))

	if len(reqBody) == 0 {
		return nil
	}

	resp, err := t.ServiceRPC(ctx, service, bytes.NewReader(reqBody), func(req *http.Request) {
		req.Header.Set("X-Entire-Push-Size", strconv.Itoa(len(reqBody)))
	})
	if err != nil {
		return fmt.Errorf("connect %s POST: %w", service, err)
	}
	defer resp.Close()

	if _, err := io.Copy(stdout, resp); err != nil {
		return fmt.Errorf("streaming response: %w", err)
	}

	return nil
}

// handleFetch handles upload-pack negotiation for connect mode by
// translating canonical git's stateful conversation into HTTP
// stateless-RPC requests against entire-server.
//
// Canonical git in connect mode acts as if it has a bidirectional
// pipe to git-upload-pack: wants + flush, then alternating have
// batches and server ACKs, terminated by done. HTTP smart-git is
// stateless: each POST to /git-upload-pack is independent and must
// carry the full wants list plus every have the client wants the
// server to consider.
//
// We bridge by buffering wants permanently, accumulating haves across
// rounds, and issuing one POST per have-batch flush. The server's
// real ACK/NAK response streams back to git, so git's MAX_IN_VAIN
// terminates on the first common ancestor instead of walking the
// entire rev queue. When git sends done, a final POST with the
// accumulated wants + haves + done streams the pack response.
func handleFetch(ctx context.Context, t Transport, stdin io.Reader, stdout io.Writer) error {
	var wantsBuf, havesBuf bytes.Buffer
	lenBuf := make([]byte, pktline.LenSize)
	pastWants := false
	roundNumber := 0

	for {
		_, err := io.ReadFull(stdin, lenBuf)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				// Git closed stdin before done. Either it aborted or
				// (more commonly) decided no fetch was needed before
				// ever sending wants. Either way, exit cleanly.
				return nil
			}
			return fmt.Errorf("reading pkt-line length: %w", err)
		}

		pktLen, err := pktline.ParseLength(lenBuf)
		if err != nil {
			return fmt.Errorf("connect fetch: %w", err)
		}

		if pktLen == pktline.Flush {
			if !pastWants {
				// End of wants section. Include the flush in the wants
				// buffer (every request body needs it as the wants
				// terminator) and switch to accumulating haves.
				wantsBuf.Write(lenBuf)
				pastWants = true
				debuglog.Printf("end of wants (%d bytes)", wantsBuf.Len())
				continue
			}
			// End of a have batch. POST wants + cumulative haves +
			// terminating flush so the server can compute ACKs.
			roundNumber++
			if err := postRound(ctx, t, stdout, &wantsBuf, &havesBuf, false, roundNumber); err != nil {
				return err
			}
			continue
		}

		if pktLen < pktline.LenSize {
			// Special pktlines (0001 delim, 0002 response-end) — not
			// expected in v0/v1 client→server stream, but tolerate by
			// pass-through to the appropriate buffer.
			dest := &wantsBuf
			if pastWants {
				dest = &havesBuf
			}
			dest.Write(lenBuf)
			continue
		}

		content := make([]byte, pktLen-pktline.LenSize)
		if _, err := io.ReadFull(stdin, content); err != nil {
			return fmt.Errorf("reading pkt-line content: %w", err)
		}

		dest := &wantsBuf
		if pastWants {
			dest = &havesBuf
		}
		dest.Write(lenBuf)
		dest.Write(content)

		if strings.TrimSpace(string(content)) == "done" {
			// Final round: server returns ACK/NAK + pack data.
			roundNumber++
			debuglog.Printf("done line received, sending final request (round %d)", roundNumber)
			return postRound(ctx, t, stdout, &wantsBuf, &havesBuf, true, roundNumber)
		}
	}
}

// postRound assembles one stateless-RPC request body from the
// buffered wants and cumulative haves, posts it to /git-upload-pack,
// and streams the response back to git on stdout. Non-final rounds
// synthesize a trailing NAK pktline so canonical git in connect
// (stateful) mode doesn't hang waiting for the round-end marker that
// the HTTP wire drops implicitly.
func postRound(ctx context.Context, t Transport, stdout io.Writer, wantsBuf, havesBuf *bytes.Buffer, final bool, roundNumber int) error {
	body := make([]byte, 0, wantsBuf.Len()+havesBuf.Len()+pktline.LenSize)
	body = append(body, wantsBuf.Bytes()...)
	body = append(body, havesBuf.Bytes()...)
	if !final {
		body = append(body, "0000"...)
	}
	debuglog.Printf("posting round %d: %d bytes (final=%v)", roundNumber, len(body), final)
	body, err := gitproto.AppendAgentToUploadPackRequest(body, Agent)
	if err != nil {
		return fmt.Errorf("amending upload-pack agent: %w", err)
	}

	resp, err := t.ServiceRPC(ctx, "git-upload-pack", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("connect fetch round %d POST: %w", roundNumber, err)
	}
	defer resp.Close()

	if _, err := io.Copy(stdout, resp); err != nil {
		return fmt.Errorf("streaming round %d response: %w", roundNumber, err)
	}
	if !final {
		if _, err := stdout.Write([]byte("0008NAK\n")); err != nil {
			return fmt.Errorf("writing NAK terminator for round %d: %w", roundNumber, err)
		}
	}
	return nil
}
