// relp-listener accepts incoming RELP connections and writes received syslog
// messages to stdout, one per line. It implements the RELP server protocol
// (version 1) directly — no external dependencies beyond the Go standard library.
//
// Designed to be used with syslog-ng's program() source:
//
//	source s_relp {
//	    program("/usr/local/bin/relp-listener --port=2514");
//	};
//
// syslog-ng reads one log line per read from the program's stdout.
// The listener acks each message via RELP before writing it.
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
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	version   = "dev"
	buildTime = ""
)

func main() {
	listenAddr := flag.String("listen", "0.0.0.0", "Listen address")
	port := flag.Int("port", 2514, "Listen port")
	useTLS := flag.Bool("tls", false, "Enable TLS")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file")
	tlsKey := flag.String("tls-key", "", "TLS private key file")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("relp-listener %s", version)
		if buildTime != "" {
			fmt.Printf(" (built %s)", buildTime)
		}
		fmt.Println()
		os.Exit(0)
	}

	addr := net.JoinHostPort(*listenAddr, strconv.Itoa(*port))

	var listener net.Listener
	var err error

	if *useTLS {
		if *tlsCert == "" || *tlsKey == "" {
			log.Fatal("relp-listener: --tls-cert and --tls-key are required when --tls is enabled")
		}
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			log.Fatalf("relp-listener: load TLS cert: %v", err)
		}
		listener, err = tls.Listen("tcp", addr, &tls.Config{
			Certificates: []tls.Certificate{cert},
		})
		if err != nil {
			log.Fatalf("relp-listener: listen %s: %v", addr, err)
		}
	} else {
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("relp-listener: listen %s: %v", addr, err)
		}
	}

	log.Printf("relp-listener: listening on %s", addr)

	// stdout writer — serialized across all connections
	stdout := &syncWriter{w: os.Stdout}

	// Graceful shutdown on SIGTERM/SIGINT
	done := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		log.Printf("relp-listener: shutting down")
		listener.Close()
		close(done)
	}()

	var wg sync.WaitGroup
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-done:
				wg.Wait()
				return
			default:
				log.Printf("relp-listener: accept: %v", err)
				continue
			}
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			handleConnection(conn, stdout)
		}()
	}
}

// syncWriter serializes writes to an io.Writer.
type syncWriter struct {
	w  io.Writer
	mu sync.Mutex
}

func (sw *syncWriter) WriteLine(line string) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	_, err := fmt.Fprintln(sw.w, line)
	return err
}

// handleConnection processes a single RELP client connection.
func handleConnection(conn net.Conn, stdout *syncWriter) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("relp-listener: connection from %s", remote)

	reader := bufio.NewReaderSize(conn, 64*1024)

	for {
		txnr, command, data, err := readFrame(reader, conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("relp-listener: %s: read: %v", remote, err)
			}
			return
		}

		switch command {
		case "open":
			rsp := "200 OK\nrelp_version=1\nrelp_software=syslog-ng-relp-listener\ncommands=syslog"
			if err := sendFrame(conn, txnr, "rsp", rsp); err != nil {
				log.Printf("relp-listener: %s: write open rsp: %v", remote, err)
				return
			}

		case "syslog":
			// Write the message to stdout for syslog-ng to read
			if err := stdout.WriteLine(data); err != nil {
				log.Printf("relp-listener: %s: stdout write: %v", remote, err)
				// Still try to ack — the RELP client expects a response
			}
			if err := sendFrame(conn, txnr, "rsp", "200 OK"); err != nil {
				log.Printf("relp-listener: %s: write syslog rsp: %v", remote, err)
				return
			}

		case "close":
			_ = sendFrame(conn, txnr, "rsp", "200 OK")
			log.Printf("relp-listener: %s: closed", remote)
			return

		default:
			// Unknown command — respond with 500
			_ = sendFrame(conn, txnr, "rsp", "500 Unknown command")
			log.Printf("relp-listener: %s: unknown command %q", remote, command)
		}
	}
}

// sendFrame writes a RELP frame to the connection.
// Format: TXNR SP COMMAND SP DATALEN [SP DATA] LF
func sendFrame(conn net.Conn, txnr int, command, data string) error {
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	var frame string
	if len(data) > 0 {
		frame = fmt.Sprintf("%d %s %d %s\n", txnr, command, len(data), data)
	} else {
		frame = fmt.Sprintf("%d %s 0\n", txnr, command)
	}
	_, err := io.WriteString(conn, frame)
	return err
}

// readFrame reads a RELP frame and returns txnr, command, and data.
func readFrame(reader *bufio.Reader, conn net.Conn) (txnr int, command string, data string, err error) {
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))

	txnrStr, err := readToken(reader)
	if err != nil {
		return 0, "", "", err
	}
	txnr, err = strconv.Atoi(txnrStr)
	if err != nil {
		return 0, "", "", fmt.Errorf("bad txnr %q: %w", txnrStr, err)
	}

	command, err = readToken(reader)
	if err != nil {
		return txnr, "", "", fmt.Errorf("read command: %w", err)
	}

	datalenStr, err := readToken(reader)
	if err != nil {
		return txnr, command, "", fmt.Errorf("read datalen: %w", err)
	}
	datalen, err := strconv.Atoi(datalenStr)
	if err != nil {
		return txnr, command, "", fmt.Errorf("bad datalen %q: %w", datalenStr, err)
	}

	if datalen > 0 {
		buf := make([]byte, datalen)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return txnr, command, "", fmt.Errorf("read data (%d bytes): %w", datalen, err)
		}
		data = string(buf)
	}

	// Read trailing LF
	b, err := reader.ReadByte()
	if err != nil {
		return txnr, command, data, fmt.Errorf("read trailer: %w", err)
	}
	if b != '\n' {
		return txnr, command, data, fmt.Errorf("expected LF trailer, got %q", b)
	}

	return txnr, command, data, nil
}

// readToken reads bytes until SP or LF, returning the token.
func readToken(reader *bufio.Reader) (string, error) {
	var token strings.Builder
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if b == ' ' {
			return token.String(), nil
		}
		if b == '\n' {
			if err := reader.UnreadByte(); err != nil {
				return "", err
			}
			return token.String(), nil
		}
		token.WriteByte(b)
	}
}
