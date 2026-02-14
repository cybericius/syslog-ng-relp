// relp-forwarder reads log lines from stdin and forwards them to a remote
// RELP server. It implements the RELP protocol (version 1) directly — no
// external dependencies beyond the Go standard library.
//
// Designed to be used with syslog-ng's program() destination:
//
//	destination d_relp {
//	    program("/usr/local/bin/relp-forwarder --host=target --port=2514");
//	};
//
// syslog-ng sends one log line per write to the program's stdin.
// The forwarder delivers each line reliably via RELP with ack confirmation.
package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	version   = "dev"
	buildTime = ""
)

func main() {
	host := flag.String("host", "localhost", "RELP server host")
	port := flag.Int("port", 2514, "RELP server port")
	useTLS := flag.Bool("tls", false, "Enable TLS")
	tlsInsecure := flag.Bool("tls-insecure", false, "Skip TLS certificate verification")
	reconnectDelay := flag.Duration("reconnect-delay", 2*time.Second, "Delay between reconnect attempts")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("relp-forwarder %s", version)
		if buildTime != "" {
			fmt.Printf(" (built %s)", buildTime)
		}
		fmt.Println()
		os.Exit(0)
	}

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))

	scanner := bufio.NewScanner(os.Stdin)
	// syslog-ng can send long lines
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var client *relpClient
	defer func() {
		if client != nil {
			client.Close()
		}
	}()

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Ensure we have a connection
		for client == nil {
			var err error
			client, err = dialRELP(addr, *useTLS, *tlsInsecure)
			if err != nil {
				log.Printf("relp-forwarder: connect %s: %v (retrying in %v)", addr, err, *reconnectDelay)
				time.Sleep(*reconnectDelay)
				continue
			}
			log.Printf("relp-forwarder: connected to %s", addr)
		}

		if err := client.SendSyslog(line); err != nil {
			log.Printf("relp-forwarder: send failed: %v (reconnecting)", err)
			client.Close()
			client = nil
			// Retry this line after reconnect
			for client == nil {
				var err error
				client, err = dialRELP(addr, *useTLS, *tlsInsecure)
				if err != nil {
					log.Printf("relp-forwarder: reconnect %s: %v (retrying in %v)", addr, err, *reconnectDelay)
					time.Sleep(*reconnectDelay)
					continue
				}
				log.Printf("relp-forwarder: reconnected to %s", addr)
			}
			// Retry the failed message
			if err := client.SendSyslog(line); err != nil {
				log.Printf("relp-forwarder: send after reconnect failed: %v", err)
				client.Close()
				client = nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("relp-forwarder: stdin read error: %v", err)
		os.Exit(1)
	}
}

// relpClient implements RELP protocol version 1.
type relpClient struct {
	conn   net.Conn
	reader *bufio.Reader
	txnr   int
	mu     sync.Mutex
}

func dialRELP(addr string, useTLS, tlsInsecure bool) (*relpClient, error) {
	var conn net.Conn
	var err error

	dialer := &net.Dialer{Timeout: 10 * time.Second}

	if useTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			InsecureSkipVerify: tlsInsecure, //nolint:gosec // user-controlled flag
		})
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	c := &relpClient{
		conn:   conn,
		reader: bufio.NewReaderSize(conn, 64*1024),
		txnr:   0,
	}

	// RELP handshake: send "open" command
	if err := c.open(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("relp open: %w", err)
	}

	return c, nil
}

func (c *relpClient) nextTxnr() int {
	c.txnr++
	if c.txnr > 999_999_999 {
		c.txnr = 1
	}
	return c.txnr
}

// sendFrame writes a RELP frame.
// Format: TXNR SP COMMAND SP DATALEN [SP DATA] LF
func (c *relpClient) sendFrame(txnr int, command, data string) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	var frame string
	if len(data) > 0 {
		frame = fmt.Sprintf("%d %s %d %s\n", txnr, command, len(data), data)
	} else {
		frame = fmt.Sprintf("%d %s 0\n", txnr, command)
	}
	_, err := io.WriteString(c.conn, frame)
	return err
}

// readFrame reads a RELP frame and returns txnr, command, and data.
// Frame format: TXNR SP COMMAND SP DATALEN [SP DATA] LF
func (c *relpClient) readFrame() (txnr int, command string, data string, err error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Read space-delimited tokens for the header: TXNR SP COMMAND SP DATALEN
	txnrStr, err := c.readToken()
	if err != nil {
		return 0, "", "", fmt.Errorf("read txnr: %w", err)
	}
	txnr, err = strconv.Atoi(txnrStr)
	if err != nil {
		return 0, "", "", fmt.Errorf("bad txnr %q: %w", txnrStr, err)
	}

	command, err = c.readToken()
	if err != nil {
		return txnr, "", "", fmt.Errorf("read command: %w", err)
	}

	datalenStr, err := c.readToken()
	if err != nil {
		return txnr, command, "", fmt.Errorf("read datalen: %w", err)
	}
	datalen, err := strconv.Atoi(datalenStr)
	if err != nil {
		return txnr, command, "", fmt.Errorf("bad datalen %q: %w", datalenStr, err)
	}

	if datalen > 0 {
		// Read the data: SP followed by exactly datalen bytes
		buf := make([]byte, datalen)
		if _, err := io.ReadFull(c.reader, buf); err != nil {
			return txnr, command, "", fmt.Errorf("read data (%d bytes): %w", datalen, err)
		}
		data = string(buf)
	}

	// Read trailing LF
	b, err := c.reader.ReadByte()
	if err != nil {
		return txnr, command, data, fmt.Errorf("read trailer: %w", err)
	}
	if b != '\n' {
		return txnr, command, data, fmt.Errorf("expected LF trailer, got %q", b)
	}

	return txnr, command, data, nil
}

// readToken reads bytes until SP or LF, returning the token.
// The delimiter (SP) is consumed; if LF is hit, it's pushed back.
func (c *relpClient) readToken() (string, error) {
	var token strings.Builder
	for {
		b, err := c.reader.ReadByte()
		if err != nil {
			return "", err
		}
		if b == ' ' {
			return token.String(), nil
		}
		if b == '\n' {
			// Token ended by LF — push back for trailer handling
			if err := c.reader.UnreadByte(); err != nil {
				return "", err
			}
			return token.String(), nil
		}
		token.WriteByte(b)
	}
}

// parseResponse extracts status code from rsp data: "200 OK\n..."
func parseResponse(data string) (code int, msg string, err error) {
	parts := strings.SplitN(data, " ", 2)
	code, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", fmt.Errorf("bad status code %q: %w", parts[0], err)
	}
	if len(parts) > 1 {
		msg = parts[1]
	}
	return code, msg, nil
}

func (c *relpClient) open() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	txnr := c.nextTxnr()
	offers := "relp_version=1\nrelp_software=syslog-ng-relp-forwarder\ncommands=syslog"
	if err := c.sendFrame(txnr, "open", offers); err != nil {
		return err
	}

	rspTxnr, command, data, err := c.readFrame()
	if err != nil {
		return fmt.Errorf("read open response: %w", err)
	}
	if command != "rsp" {
		return fmt.Errorf("expected rsp, got %q", command)
	}
	if rspTxnr != txnr {
		return fmt.Errorf("txnr mismatch: sent %d, got %d", txnr, rspTxnr)
	}

	code, _, err := parseResponse(data)
	if err != nil {
		return fmt.Errorf("parse open response: %w", err)
	}
	if code != 200 {
		return fmt.Errorf("open rejected with code %d", code)
	}

	return nil
}

// SendSyslog sends a single log message via RELP and waits for ack.
func (c *relpClient) SendSyslog(message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	txnr := c.nextTxnr()
	if err := c.sendFrame(txnr, "syslog", message); err != nil {
		return err
	}

	rspTxnr, command, data, err := c.readFrame()
	if err != nil {
		return fmt.Errorf("read syslog response: %w", err)
	}
	if command != "rsp" {
		return fmt.Errorf("expected rsp, got %q", command)
	}
	if rspTxnr != txnr {
		return fmt.Errorf("txnr mismatch: sent %d, got %d", txnr, rspTxnr)
	}

	code, _, err := parseResponse(data)
	if err != nil {
		return fmt.Errorf("parse syslog response: %w", err)
	}
	if code != 200 {
		return fmt.Errorf("syslog rejected with code %d", code)
	}

	return nil
}

// Close sends RELP close and shuts down the connection.
func (c *relpClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return
	}

	txnr := c.nextTxnr()
	// Best-effort close — don't block on errors
	_ = c.sendFrame(txnr, "close", "")
	// Try to read close response but don't fail if it times out
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, _, _ = c.readFrame()
	c.conn.Close()
	c.conn = nil
}
