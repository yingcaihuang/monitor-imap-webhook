package imapclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	imap "github.com/emersion/go-imap"
	idle "github.com/emersion/go-imap-idle"
	"github.com/emersion/go-imap/client"

	"monitor-imap-webhook/internal/config"
)

// Event represents a new message arrival (UID).
type Event struct{ UID uint32 }

// OpStat aggregates operation metrics.
type OpStat struct {
	Count         int
	Errors        int
	LastDuration  time.Duration
	TotalDuration time.Duration
	LastError     string
}

// Client wraps an IMAP client with reconnect, IDLE handling, command serialization and stats.
type Client struct {
	cfg    *config.Config
	log    *log.Logger
	mu     sync.Mutex
	c      *client.Client
	closed bool

	// command serialization & stats
	cmdMu   sync.Mutex
	statsMu sync.Mutex
	opStats map[string]*OpStat

	// track active external processing (body fetch / parse / webhook) so we delay re-entering IDLE
	activeMu      sync.Mutex
	activeFetches int
}

func New(cfg *config.Config) *Client {
	return &Client{cfg: cfg, log: log.New(log.Writer(), "imapclient ", log.LstdFlags|log.Lmicroseconds), opStats: make(map[string]*OpStat)}
}

// Connect establishes IMAP connection (TLS or STARTTLS) and selects mailbox.
func (cl *Client) Connect(ctx context.Context) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.closed {
		return errors.New("client closed")
	}
	if cl.c != nil {
		return nil
	}

	addr := fmt.Sprintf("%s:%d", cl.cfg.IMAPHost, cl.cfg.IMAPPort)
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var c *client.Client
	var err error
	if cl.cfg.UseTLS {
		c, err = client.DialWithDialerTLS(dialer, addr, &tls.Config{ServerName: cl.cfg.IMAPHost, InsecureSkipVerify: cl.cfg.InsecureSkipVerify})
	} else {
		c, err = client.DialWithDialer(dialer, addr)
		if err == nil && cl.cfg.StartTLS {
			if cl.cfg.Debug {
				cl.log.Printf("starting TLS upgrade")
			}
			if err = c.StartTLS(&tls.Config{ServerName: cl.cfg.IMAPHost, InsecureSkipVerify: cl.cfg.InsecureSkipVerify}); err != nil {
				c.Logout()
				return fmt.Errorf("starttls: %w", err)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	if cl.cfg.Debug {
		cl.log.Printf("dial ok %s", addr)
	}
	if err = c.Login(cl.cfg.Username, cl.cfg.Password); err != nil {
		c.Logout()
		return fmt.Errorf("login: %w", err)
	}
	if cl.cfg.Debug {
		cl.log.Printf("login ok user=%s", cl.cfg.Username)
	}
	if _, err = c.Select(cl.cfg.Mailbox, false); err != nil {
		c.Logout()
		return fmt.Errorf("select mailbox: %w", err)
	}
	cl.c = c
	return nil
}

// Close logs out and marks client closed.
func (cl *Client) Close() error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.closed = true
	if cl.c != nil {
		return cl.c.Logout()
	}
	return nil
}

// BeginProcess increments active processing counter (called when emitting an Event).
func (cl *Client) BeginProcess() { cl.activeMu.Lock(); cl.activeFetches++; cl.activeMu.Unlock() }

// EndProcess decrements active processing counter (called by consumer after processing done).
func (cl *Client) EndProcess() {
	cl.activeMu.Lock()
	if cl.activeFetches > 0 {
		cl.activeFetches--
	}
	cl.activeMu.Unlock()
}

// drain waits until all active processing finished or DrainTimeout reached.
func (cl *Client) drain(ctx context.Context) {
	if cl.cfg.DrainTimeout <= 0 {
		return
	}
	deadline := time.Now().Add(cl.cfg.DrainTimeout)
	for {
		cl.activeMu.Lock()
		n := cl.activeFetches
		cl.activeMu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			if cl.cfg.Debug {
				cl.log.Printf("drain timeout active=%d", n)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// IdleLoop detects new messages and emits Events (with UID) serialized with IDLE state.
func (cl *Client) IdleLoop(ctx context.Context, events chan<- Event) error {
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := cl.Connect(ctx); err != nil {
			cl.log.Printf("connect error: %v", err)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		status, err := cl.status()
		if err != nil {
			cl.reset("status err", err)
			continue
		}
		baseline := status.Messages
		if cl.cfg.Debug {
			cl.log.Printf("mailbox selected messages=%d", baseline)
		}

		updates := make(chan client.Update, 50)
		cl.mu.Lock()
		if cl.c != nil {
			cl.c.Updates = updates
		}
		cl.mu.Unlock()

		var ticker *time.Ticker
		if cl.cfg.CheckInterval > 0 {
			ticker = time.NewTicker(cl.cfg.CheckInterval)
			defer ticker.Stop()
		}

	IDLE_START:
		if err := ctx.Err(); err != nil {
			return err
		}
		idleClient := idle.NewClient(cl.Raw())
		stop := make(chan struct{})
		done := make(chan error, 1)
		if cl.cfg.Debug {
			cl.log.Printf("enter IDLE baseline=%d", baseline)
		}

		// keepalive for very long idle sessions
		go func(stopCh <-chan struct{}) {
			for {
				select {
				case <-ctx.Done():
					return
				case <-stopCh:
					return
				case <-time.After(25 * time.Minute):
					cl.mu.Lock()
					if cl.c != nil {
						_ = cl.c.Noop()
					}
					cl.mu.Unlock()
				}
			}
		}(stop)

		// start IDLE
		go func() {
			if cl.cfg.Debug {
				cl.log.Printf("idle started")
			}
			err := idleClient.IdleWithFallback(stop, 0)
			if cl.cfg.Debug {
				cl.log.Printf("idle finished err=%v", err)
			}
			done <- err
		}()

		for {
			select {
			case <-ctx.Done():
				close(stop)
				<-done
				return ctx.Err()
			case err := <-done: // IDLE ended
				if err != nil { // connection issue
					if cl.cfg.Debug {
						cl.log.Printf("idle end with error: %v", err)
					}
					cl.reset("idle error", err)
					time.Sleep(2 * time.Second)
					goto RECONNECT
				}
				if cl.cfg.Debug {
					cl.log.Printf("idle end normal restart")
				}
				goto IDLE_START
			case upd := <-updates:
				if mboxUpd, ok := upd.(*client.MailboxUpdate); ok && mboxUpd.Mailbox != nil {
					if mboxUpd.Mailbox.Messages > baseline {
						if cl.cfg.Debug {
							cl.log.Printf("MailboxUpdate messages=%d baseline=%d", mboxUpd.Mailbox.Messages, baseline)
						}
						if cl.handleNewMessages(ctx, mboxUpd.Mailbox.Messages, &baseline, stop, done, events) {
							cl.drain(ctx)
							goto IDLE_START
						}
					}
					continue
				}
				if msgUpd, ok := upd.(*client.MessageUpdate); ok && msgUpd.Message != nil { // fallback: query status
					if cl.cfg.Debug {
						cl.log.Printf("MessageUpdate seq=%d baseline=%d", msgUpd.Message.SeqNum, baseline)
					}
					close(stop)
					if err := <-done; err != nil {
						cl.reset("idle exit after message update", err)
						time.Sleep(2 * time.Second)
						goto RECONNECT
					}
					st, err := cl.status()
					if err != nil {
						cl.reset("status after message update", err)
						time.Sleep(2 * time.Second)
						goto RECONNECT
					}
					if st.Messages > baseline {
						if cl.handleNewMessages(ctx, st.Messages, &baseline, nil, nil, events) {
							cl.drain(ctx)
						}
					}
					goto IDLE_START
				}
			case <-tickerC(ticker):
				if cl.cfg.Debug {
					cl.log.Printf("poll tick baseline=%d", baseline)
				}
				close(stop)
				if err := <-done; err != nil {
					cl.reset("idle exit on poll", err)
					time.Sleep(2 * time.Second)
					goto RECONNECT
				}
				st, err := cl.status()
				if err != nil {
					cl.reset("status poll", err)
					time.Sleep(2 * time.Second)
					goto RECONNECT
				}
				if st.Messages > baseline {
					if cl.cfg.Debug {
						cl.log.Printf("poll detected new messages=%d baseline=%d", st.Messages, baseline)
					}
					if cl.handleNewMessages(ctx, st.Messages, &baseline, nil, nil, events) {
						cl.drain(ctx)
					}
				}
				goto IDLE_START
			}
		}
	RECONNECT:
		time.Sleep(2 * time.Second)
	}
}

// status re-selects mailbox to get message count.
func (cl *Client) status() (*imap.MailboxStatus, error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.c.Select(cl.cfg.Mailbox, false)
}

// reset closes the connection so Loop can reconnect.
func (cl *Client) reset(reason string, err error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.c != nil {
		_ = cl.c.Logout()
		cl.c = nil
	}
	cl.log.Printf("reset connection: %s: %v", reason, err)
}

// Raw returns underlying client (read-only usage with external synchronization).
func (cl *Client) Raw() *client.Client { cl.mu.Lock(); defer cl.mu.Unlock(); return cl.c }

// Exec serializes IMAP commands with timeout + stats tracking.
func (cl *Client) Exec(ctx context.Context, op string, fn func(c *client.Client) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	const defaultTimeout = 15 * time.Second
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	start := time.Now()
	cl.cmdMu.Lock()
	cl.mu.Lock()
	c := cl.c
	cl.mu.Unlock()
	if c == nil {
		cl.cmdMu.Unlock()
		return errors.New("no connection")
	}
	errCh := make(chan error, 1)
	go func() { errCh <- fn(c) }()
	var err error
	select {
	case <-ctx.Done():
		err = fmt.Errorf("op %s timeout or canceled: %w", op, ctx.Err())
	case e := <-errCh:
		err = e
	}
	dur := time.Since(start)
	cl.updateStats(op, dur, err)
	if cl.cfg.Debug {
		if err != nil {
			cl.log.Printf("op=%s dur=%s err=%v", op, dur, err)
		} else {
			cl.log.Printf("op=%s dur=%s ok", op, dur)
		}
	}
	cl.cmdMu.Unlock()
	return err
}

func (cl *Client) updateStats(op string, d time.Duration, err error) {
	cl.statsMu.Lock()
	st, ok := cl.opStats[op]
	if !ok {
		st = &OpStat{}
		cl.opStats[op] = st
	}
	st.Count++
	st.LastDuration = d
	st.TotalDuration += d
	if err != nil {
		st.Errors++
		st.LastError = err.Error()
	} else {
		st.LastError = ""
	}
	cl.statsMu.Unlock()
}

// Stats returns snapshot of operation stats.
func (cl *Client) Stats() map[string]OpStat {
	cl.statsMu.Lock()
	defer cl.statsMu.Unlock()
	out := make(map[string]OpStat, len(cl.opStats))
	for k, v := range cl.opStats {
		out[k] = *v
	}
	return out
}

// handleNewMessages fetches new UIDs (range baseline+1 .. newTotal) and emits events.
// It increments activeFetches per emitted UID so IdleLoop can delay IDLE restart until processing done.
func (cl *Client) handleNewMessages(ctx context.Context, newTotal uint32, baseline *uint32, stop chan struct{}, done chan error, events chan<- Event) bool {
	if newTotal <= *baseline {
		return false
	}
	newCount := newTotal - *baseline
	if cl.cfg.Debug {
		cl.log.Printf("handleNewMessages newTotal=%d baseline=%d newCount=%d", newTotal, *baseline, newCount)
	}
	// exit IDLE if currently idling
	if stop != nil && done != nil {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		close(stop)
		if err := <-done; err != nil {
			cl.reset("idle exit before fetch", err)
			time.Sleep(2 * time.Second)
			return false
		}
	}
	// build sequence
	seq := new(imap.SeqSet)
	seq.AddRange(*baseline+1, newTotal)
	items := []imap.FetchItem{imap.FetchUid}
	ch := make(chan *imap.Message, newCount)
	if err := cl.Exec(ctx, "fetch-uids", func(c *client.Client) error { return c.Fetch(seq, items, ch) }); err != nil {
		cl.log.Printf("fetch uids error: %v", err)
	} else {
		for msg := range ch {
			if cl.cfg.Debug {
				cl.log.Printf("emit uid=%d", msg.Uid)
			}
			cl.BeginProcess()
			select {
			case events <- Event{UID: msg.Uid}:
			case <-ctx.Done():
				cl.EndProcess()
			}
		}
	}
	*baseline = newTotal
	if cl.cfg.Debug {
		cl.log.Printf("baseline updated=%d", *baseline)
	}
	return true
}

// tickerC safely returns ticker.C or nil.
func tickerC(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}
