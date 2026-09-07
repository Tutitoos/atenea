package activity

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Environment is part of ATENEA's public orchestration contract.
const Environment = "ATENEA_ACTIVITY_SOCKET"

// Notice contains intent only. Tool arguments and results never cross this
// channel, which keeps credentials and large payloads out of chat activity.
type Notice struct {
	ID        string `json:"id"`
	Tool      string `json:"tool"`
	Action    string `json:"action"`
	Objective string `json:"objective"`
	Purpose   string `json:"purpose"`
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Server is an invocation-scoped Unix socket. A successful reply is an
// acknowledgement that the host callback completed before the tool proceeds.
type Server struct {
	path     string
	dir      string
	listener net.Listener
	done     chan struct{}
	once     sync.Once
	mu       sync.Mutex
	closing  bool
	conns    map[net.Conn]struct{}
	handlers sync.WaitGroup
	requests chan activityRequest
	batched  chan struct{}
}

type activityRequest struct {
	notice Notice
	reply  chan error
}

// Start is part of ATENEA's public orchestration contract.
func Start(callback func([]Notice) error) (*Server, error) {
	if callback == nil {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "atenea-act-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	path := filepath.Join(dir, "activity.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	s := &Server{path: path, dir: dir, listener: listener, done: make(chan struct{}), conns: make(map[net.Conn]struct{}),
		requests: make(chan activityRequest, 64), batched: make(chan struct{})}
	go s.batch(callback)
	go s.serve()
	return s, nil
}

// Path is part of ATENEA's public orchestration contract.
func (s *Server) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Close is part of ATENEA's public orchestration contract.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.once.Do(func() {
		s.mu.Lock()
		s.closing = true
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		err = s.listener.Close()
		<-s.done
		s.handlers.Wait()
		close(s.requests)
		<-s.batched
		if removeErr := os.RemoveAll(s.dir); err == nil {
			err = removeErr
		}
	})
	return err
}

func (s *Server) serve() {
	defer close(s.done)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.handlers.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.handlers.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
			}()
			s.handle(conn)
		}()
	}
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	var notice Notice
	err := json.NewDecoder(bufio.NewReader(conn)).Decode(&notice)
	if err == nil {
		if notice.ID == "" || strings.TrimSpace(notice.Tool) == "" {
			err = errors.New("activity notice requires id and tool")
		} else {
			reply := make(chan error, 1)
			s.requests <- activityRequest{notice: notice, reply: reply}
			err = <-reply
		}
	}
	reply := response{OK: err == nil}
	if err != nil {
		reply.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(reply)
}

func (s *Server) batch(callback func([]Notice) error) {
	defer close(s.batched)
	for first := range s.requests {
		batch := []activityRequest{first}
		timer := time.NewTimer(3 * time.Millisecond)
	collect:
		for {
			select {
			case next, ok := <-s.requests:
				if !ok {
					break collect
				}
				batch = append(batch, next)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		notices := make([]Notice, 0, len(batch))
		for _, request := range batch {
			notices = append(notices, request.notice)
		}
		err := callback(notices)
		for _, request := range batch {
			request.reply <- err
		}
	}
}

// Publish reports one pre-tool intent to the parent and waits for its ack.
func Publish(socket, tool string) error {
	socket, tool = strings.TrimSpace(socket), strings.TrimSpace(tool)
	if socket == "" {
		return nil
	}
	if tool == "" {
		return errors.New("activity tool is empty")
	}
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return fmt.Errorf("activity channel unavailable: %w", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	notice := Notice{ID: newID(), Tool: tool, Action: "uso", Objective: "la herramienta autorizada", Purpose: "completar el paso delegado"}
	if err := json.NewEncoder(conn).Encode(notice); err != nil {
		return fmt.Errorf("activity publish: %w", err)
	}
	var reply response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&reply); err != nil {
		return fmt.Errorf("activity acknowledgement: %w", err)
	}
	if !reply.OK {
		return fmt.Errorf("activity rejected: %s", reply.Error)
	}
	return nil
}

// PublishFromEnvironment is part of ATENEA's public orchestration contract.
func PublishFromEnvironment(tool string) error { return Publish(os.Getenv(Environment), tool) }

func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}
