package pty

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Python PTY bridge that allocates a real macOS/Linux pseudo-terminal with TIOCSWINSZ resize support.
const pyPTYHelper = `
import os, sys, pty, select, struct, fcntl, termios, json, signal

shell = os.environ.get("SHELL", "/bin/zsh")
init_cmd = sys.argv[1] if len(sys.argv) > 1 else ""

pid, master_fd = pty.fork()
if pid == 0:
    os.environ["TERM"] = "xterm-256color"
    os.environ["COLORTERM"] = "truecolor"
    os.execvp(shell, [shell, "-l"])

def set_winsize(fd, rows, cols):
    try:
        s = struct.pack("HHHH", rows, cols, 0, 0)
        fcntl.ioctl(fd, termios.TIOCSWINSZ, s)
    except Exception:
        pass

set_winsize(master_fd, 34, 115)

if init_cmd:
    os.write(master_fd, (init_cmd + "\r").encode("utf-8"))

stdin_fd = sys.stdin.fileno()
stdout_fd = sys.stdout.fileno()

try:
    while True:
        r, _, _ = select.select([master_fd, stdin_fd], [], [])
        if master_fd in r:
            try:
                data = os.read(master_fd, 8192)
            except OSError:
                break
            if not data:
                break
            # Frame as length-prefixed binary packet to Go parent
            hdr = struct.pack(">I", len(data))
            os.write(stdout_fd, hdr + data)
        if stdin_fd in r:
            hdr = os.read(stdin_fd, 5)
            if not hdr or len(hdr) < 5:
                break
            pkt_type = hdr[0]
            pkt_len = struct.unpack(">I", hdr[1:5])[0]
            buf = b""
            while len(buf) < pkt_len:
                c = os.read(stdin_fd, pkt_len - len(buf))
                if not c:
                    break
                buf += c
            if pkt_type == 1: # Raw terminal input
                os.write(master_fd, buf)
            elif pkt_type == 2: # Resize JSON {"cols":C,"rows":R}
                try:
                    j = json.loads(buf.decode("utf-8"))
                    set_winsize(master_fd, int(j.get("rows", 30)), int(j.get("cols", 100)))
                except Exception:
                    pass
finally:
    try:
        os.kill(pid, signal.SIGTERM)
    except Exception:
        pass
`

// HandleWebSocket upgrades /api/pty to a WebSocket and bridges it to a real local PTY session.
func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "WebSocket upgrade required", http.StatusBadRequest)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()

	h := sha1.New()
	_, _ = io.WriteString(h, key+wsGUID)
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}

	initCmd := r.URL.Query().Get("cmd")
	home, _ := os.UserHomeDir()
	cmd := exec.Command("python3", "-u", "-c", pyPTYHelper, initCmd)
	if home != "" {
		cmd.Dir = home
	}
	env := os.Environ()
	extraPaths := fmt.Sprintf("%s/.opencode/bin:%s/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", home, home)
	env = append(env, "PATH="+extraPaths+":"+os.Getenv("PATH"))
	cmd.Env = env

	ptyIn, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	ptyOut, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	defer func() {
		_ = ptyIn.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	var writeMu sync.Mutex
	sendWSFrame := func(opcode byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()

		var hdr []byte
		hdr = append(hdr, 0x80|opcode)
		l := len(payload)
		if l < 126 {
			hdr = append(hdr, byte(l))
		} else if l < 65536 {
			hdr = append(hdr, 126, byte(l>>8), byte(l))
		} else {
			hdr = append(hdr, 127,
				byte(l>>56), byte(l>>48), byte(l>>40), byte(l>>32),
				byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
		}
		if _, err := conn.Write(hdr); err != nil {
			return err
		}
		_, err := conn.Write(payload)
		return err
	}

	// Pump PTY stdout -> WebSocket
	done := make(chan struct{})
	go func() {
		defer close(done)
		lenBuf := make([]byte, 4)
		for {
			if _, err := io.ReadFull(ptyOut, lenBuf); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(lenBuf)
			if n == 0 || n > 1024*1024 {
				continue
			}
			data := make([]byte, n)
			if _, err := io.ReadFull(ptyOut, data); err != nil {
				return
			}
			// Send as WebSocket Binary Frame (0x2) so WKWebView never aborts on split multi-byte UTF-8 sequences
			if err := sendWSFrame(0x2, data); err != nil {
				return
			}
		}
	}()

	// Pump WebSocket -> PTY stdin
	go func() {
		for {
			opcode, payload, err := readWSFrame(rw.Reader)
			if err != nil {
				_ = conn.Close()
				return
			}
			if opcode == 0x8 { // Close
				_ = conn.Close()
				return
			}
			if opcode == 0x9 { // Ping
				_ = sendWSFrame(0xA, payload)
				continue
			}
			if len(payload) == 0 {
				continue
			}

			// Prefix protocol from frontend:
			// '0' + raw stdin text OR '1' + resize JSON {"cols":C,"rows":R}
			kind := byte(1)
			body := payload
			if payload[0] == '0' {
				kind = 1
				body = payload[1:]
			} else if payload[0] == '1' {
				kind = 2
				body = payload[1:]
			}

			pkt := make([]byte, 5+len(body))
			pkt[0] = kind
			binary.BigEndian.PutUint32(pkt[1:5], uint32(len(body)))
			copy(pkt[5:], body)
			if _, err := ptyIn.Write(pkt); err != nil {
				return
			}
		}
	}()

	<-done
}

func readWSFrame(r *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	opcode := h[0] & 0x0F
	masked := (h[1] & 0x80) != 0
	length := uint64(h[1] & 0x7F)

	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	} else if length == 127 {
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

// Unused import guard for net
var _ net.Conn
