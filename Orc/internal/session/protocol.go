package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"

	"orc/common/fault"
	"orc/orc/internal/pty"
)

// The socket protocol is one JSON line in, then whatever that verb needs.
//
// It is deliberately not a general RPC. There are four things another process wants
// from a live session — watch it, type into it, ask how it is, stop it — and a
// line-delimited request keeps the client small enough that `orc attach --direct`
// is a proxy loop rather than a protocol implementation.
//
// Attach is the one verb that changes the shape of the connection: after the
// handshake the socket carries raw terminal bytes in both directions, because that
// is what a terminal is. A framed attach would mean every keystroke paying for a
// header, and a resize arriving mid-escape-sequence corrupting the screen. Resize
// is therefore its own short connection rather than a message inside the stream —
// which is also why the client can be a dumb copy loop.

// Op is a request verb.
type Op string

// The verbs.
const (
	OpAttach Op = "attach"
	OpPoke   Op = "poke"
	OpStatus Op = "status"
	OpStop   Op = "stop"
	OpResize Op = "resize"
)

// Request is the JSON line a client sends first.
type Request struct {
	Op   Op     `json:"op"`
	Text string `json:"text,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
}

// Reply is the JSON line the server sends back for everything but attach.
type Reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Status fields, for OpStatus.
	ID       string `json:"id,omitempty"`
	Child    int    `json:"child,omitempty"`
	Restarts int    `json:"restarts,omitempty"`
	Live     bool   `json:"live,omitempty"`
}

// HandshakeDeadline bounds how long the server waits for a client's first line. A
// connection that opens and says nothing is a stuck client or a port scan, and
// either way the session should not keep a goroutine for it.
const HandshakeDeadline = 5 * time.Second

// serve accepts connections until the listener closes.
func (s *Supervisor) serve(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return // the listener closed: the supervisor is going
		}
		go s.handle(conn)
	}
}

// handle answers one connection.
func (s *Supervisor) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	if err := conn.SetReadDeadline(time.Now().Add(HandshakeDeadline)); err != nil {
		return
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}

	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeReply(conn, Reply{Error: "the request was not json"})
		return
	}

	switch req.Op {
	case OpAttach:
		// No deadline from here: an attach is a person watching, and one that timed
		// out mid-session would look like the agent had died.
		_ = conn.SetReadDeadline(time.Time{})
		s.serveAttach(conn, reader, req)

	case OpPoke:
		err := s.Poke(req.Text)
		_ = writeReply(conn, replyFor(err))

	case OpResize:
		err := s.Resize(pty.WinSize{Rows: req.Rows, Cols: req.Cols})
		_ = writeReply(conn, replyFor(err))

	case OpStatus:
		s.mu.Lock()
		child := 0
		if s.child != nil && s.child.Process != nil {
			child = s.child.Process.Pid
		}
		reply := Reply{OK: true, ID: s.spec.ID, Child: child, Restarts: s.restarts, Live: s.pty != nil}
		s.mu.Unlock()
		_ = writeReply(conn, reply)

	case OpStop:
		// Answered before stopping, because stopping tears down the thing the
		// answer would travel over.
		_ = writeReply(conn, Reply{OK: true})
		s.Stop()

	default:
		_ = writeReply(conn, Reply{Error: "unknown op " + string(req.Op)})
	}
}

// serveAttach streams the session to a client and its keystrokes back.
//
// The scrollback goes first, so an attach shows what happened before it arrived
// rather than an empty screen that reads as a hung agent.
func (s *Supervisor) serveAttach(conn net.Conn, buffered *bufio.Reader, req Request) {
	if req.Rows > 0 && req.Cols > 0 {
		// The attaching terminal's size wins: it is the one somebody is looking at.
		_ = s.Resize(pty.WinSize{Rows: req.Rows, Cols: req.Cols})
	}

	out, history, stop := s.watch()
	defer stop()

	if len(history) > 0 {
		if _, err := conn.Write(history); err != nil {
			return
		}
	}

	// Keystrokes, including anything the handshake read left buffered.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4<<10)
		for {
			n, err := buffered.Read(buf)
			if n > 0 {
				if err := s.write(buf[:n]); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case chunk := <-out:
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

func writeReply(w io.Writer, reply Reply) error {
	data, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

func replyFor(err error) Reply {
	if err != nil {
		return Reply{Error: err.Error()}
	}
	return Reply{OK: true}
}

// Client talks to a supervisor's socket.
type Client struct {
	path string
}

// Dial connects to a session's socket.
//
// A socket that is not there, or that nothing is listening on, is reported as
// *unavailable* rather than as missing: the identity exists and the session does
// not, and the caller — `orc poke`, `orc attach` — wants to be told which.
func Dial(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, fault.Unavailable{Peer: "the session at " + path, Err: err}
	}
	_ = conn.Close()
	return &Client{path: path}, nil
}

// send opens a connection, sends one request, and returns it for the caller to
// carry on with. Every verb gets its own connection, which is what makes a resize
// during an attach possible without multiplexing.
func (c *Client) send(req Request) (net.Conn, *bufio.Reader, error) {
	conn, err := net.DialTimeout("unix", c.path, 2*time.Second)
	if err != nil {
		return nil, nil, fault.Unavailable{Peer: "the session at " + c.path, Err: err}
	}
	data, err := json.Marshal(req)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fault.Internal{Where: "session.send", Detail: err.Error()}
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		_ = conn.Close()
		return nil, nil, fault.Unavailable{Peer: "the session at " + c.path, Err: err}
	}
	return conn, bufio.NewReader(conn), nil
}

// ask sends a request and reads the one-line reply.
func (c *Client) ask(req Request) (Reply, error) {
	conn, reader, err := c.send(req)
	if err != nil {
		return Reply{}, err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetReadDeadline(time.Now().Add(HandshakeDeadline)); err != nil {
		return Reply{}, err
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return Reply{}, fault.Unavailable{Peer: "the session", Err: err}
	}
	var reply Reply
	if err := json.Unmarshal(line, &reply); err != nil {
		return Reply{}, fault.Parse{Path: c.path, Reason: "the session replied with something that is not json"}
	}
	if !reply.OK {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

// Poke types a message into the session.
func (c *Client) Poke(text string) error {
	_, err := c.ask(Request{Op: OpPoke, Text: text})
	return err
}

// Resize tells the session how big the attached terminal is now.
func (c *Client) Resize(size pty.WinSize) error {
	_, err := c.ask(Request{Op: OpResize, Rows: size.Rows, Cols: size.Cols})
	return err
}

// Status asks what the session is doing.
func (c *Client) Status() (Reply, error) { return c.ask(Request{Op: OpStatus}) }

// Stop asks the supervisor to end the session and exit.
func (c *Client) Stop() error {
	_, err := c.ask(Request{Op: OpStop})
	return err
}

// Attach opens a raw stream to the session: everything read from it is terminal
// output, and everything written to it is keystrokes.
//
// The caller owns the connection and closes it to detach. That is what makes
// detaching cost nothing to the session — the supervisor sees a closed socket,
// drops the watcher, and the agent carries on.
func (c *Client) Attach(size pty.WinSize) (net.Conn, error) {
	conn, _, err := c.send(Request{Op: OpAttach, Rows: size.Rows, Cols: size.Cols})
	if err != nil {
		return nil, err
	}
	return conn, nil
}
