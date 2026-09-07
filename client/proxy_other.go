//go:build !js

package main

// SOCKS5 transport for dialWS: raw TCP to the proxy, RFC 1928
// handshake, TLS when the target is wss, then the WebSocket
// handshake over the established stream via gorilla NewClient.

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/passprompt"
	"github.com/CleveTok3125/V2V/internal/tui"
)

// socks5DialTimeout bounds the whole TCP + handshake + TLS setup.
const socks5DialTimeout = 45 * time.Second

// dialSocks5WS connects to a ws/wss URL through a SOCKS5 proxy. The
// target hostname is never resolved locally; the proxy resolves it.
// The proxy password wipes from the config once dialing succeeds.
func dialSocks5WS(wsURL string, headers http.Header, p *proxyConfig) (wsConn, *http.Response, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, nil, fmt.Errorf("URL server không hợp lệ: %w", err)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "wss" {
			port = "443"
		}
	}
	targetPort, err := strconv.Atoi(port)
	if err != nil || targetPort < 1 || targetPort > 65535 {
		return nil, nil, fmt.Errorf("port server không hợp lệ: %q", port)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)), socks5DialTimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("không tới được proxy: %w", err)
	}
	// Fail closed on any setup error: never leak a half-open socket.
	failed := true
	defer func() {
		if failed {
			_ = conn.Close()
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(socks5DialTimeout)); err != nil {
		return nil, nil, err
	}
	if err := socks5Handshake(conn, host, targetPort, []byte(p.User), p.Pass); err != nil {
		return nil, nil, err
	}
	p.wipe()
	stream := net.Conn(conn)
	if u.Scheme == "wss" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.Handshake(); err != nil {
			return nil, nil, fmt.Errorf("TLS qua proxy thất bại: %w", err)
		}
		stream = tlsConn
	}
	if err := stream.SetDeadline(time.Time{}); err != nil {
		return nil, nil, err
	}
	c, resp, err := websocket.NewClient(stream, u, headers, 4096, 4096)
	if err != nil {
		return nil, resp, err
	}
	failed = false
	return c, resp, nil
}

// resolveProxy picks the proxy for this session. Precedence: the
// --ask-proxy wizard beats everything; then --proxy flag, then
// V2V_PROXY env. Nil means direct: gorilla's DefaultDialer still
// honors the system HTTP(S)_PROXY/NO_PROXY on its own.
func resolveProxy(r io.Reader) (*proxyConfig, error) {
	if CLI.AskProxy {
		return promptProxyConfig(r)
	}
	if strings.TrimSpace(CLI.Proxy) != "" {
		return parseProxyURL(CLI.Proxy)
	}
	if env := strings.TrimSpace(os.Getenv("V2V_PROXY")); env != "" {
		return parseProxyURL(env)
	}
	return nil, nil
}

// wizardLine prints a prompt and reads one trimmed line. A closed
// reader aborts instead of looping forever; plain Enter yields "".
func wizardLine(r io.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := readLineRaw(r)
	if err != nil && len(line) == 0 {
		return "", errors.New("stdin đóng giữa lúc nhập proxy")
	}
	return strings.TrimSpace(line), nil
}

// promptProxyConfig runs the --ask-proxy wizard: scheme select, host,
// port (scheme default), optional username, and a hidden password
// prompt only when a username was given. The password gets no meter
// and no weak gate: it belongs to the proxy, not to this project.
// Results stay in RAM for this session.
func promptProxyConfig(r io.Reader) (*proxyConfig, error) {
	if tui.Interactive() {
		return promptProxyConfigHuh()
	}
	return promptProxyConfigPiped(r)
}

// promptProxyConfigHuh runs the wizard as huh forms: scheme select,
// one 3-field form for host/port/user with inline validation, then
// the hidden password prompt when a username was given.
func promptProxyConfigHuh() (*proxyConfig, error) {
	idx, err := promptProxyScheme(os.Stdin)
	if err != nil {
		return nil, err
	}
	scheme := []string{"http", "socks5", "https"}[idx]
	cfg := &proxyConfig{Scheme: scheme}
	var host, user string
	portStr := strconv.Itoa(defaultProxyPort(scheme))
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Proxy host").Value(&host).Validate(nonEmptyLine),
		huh.NewInput().Title("Proxy port").Value(&portStr).Validate(validPort),
		huh.NewInput().Title("Proxy username (trống = không auth)").Value(&user),
	))
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, tui.ErrAborted
		}
		return nil, err
	}
	cfg.Host = strings.TrimSpace(host)
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("proxy port không hợp lệ: %q", portStr)
	}
	cfg.Port = port
	cfg.User = strings.TrimSpace(user)
	if cfg.User == "" {
		return cfg, nil
	}
	pass, err := passprompt.Password(passprompt.PasswordOpts{
		Title:      "Proxy password",
		AllowEmpty: true,
	})
	if err != nil {
		return nil, err
	}
	cfg.Pass = []byte(pass)
	return cfg, nil
}

// nonEmptyLine rejects blank input.
func nonEmptyLine(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("không được để trống")
	}
	return nil
}

// validPort rejects anything outside 1-65535.
func validPort(s string) error {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return errors.New("port phải là số 1-65535")
	}
	return nil
}

func promptProxyConfigPiped(r io.Reader) (*proxyConfig, error) {
	idx, err := promptProxyScheme(r)
	if err != nil {
		return nil, err
	}
	scheme := []string{"http", "socks5", "https"}[idx]
	cfg := &proxyConfig{Scheme: scheme}
	for {
		host, err := wizardLine(r, "Proxy host: ")
		if err != nil {
			return nil, err
		}
		if host != "" {
			cfg.Host = host
			break
		}
		fmt.Println("❌ Host trống, nhập lại.")
	}
	defPort := defaultProxyPort(scheme)
	for {
		raw, err := wizardLine(r, fmt.Sprintf("Proxy port (Enter = %d): ", defPort))
		if err != nil {
			return nil, err
		}
		if raw == "" {
			cfg.Port = defPort
			break
		}
		p, err := strconv.Atoi(raw)
		if err != nil || p < 1 || p > 65535 {
			fmt.Println("❌ Port phải là số 1-65535, nhập lại.")
			continue
		}
		cfg.Port = p
		break
	}
	user, err := wizardLine(r, "Proxy username (trống = không auth): ")
	if err != nil {
		return nil, err
	}
	cfg.User = user
	if user == "" {
		return cfg, nil
	}
	if tui.Interactive() {
		pass, err := passprompt.Password(passprompt.PasswordOpts{
			Title:      "Proxy password",
			AllowEmpty: true,
		})
		if err != nil {
			return nil, err
		}
		cfg.Pass = []byte(pass)
		return cfg, nil
	}
	pass, err := wizardLine(r, "Proxy password: ")
	if err != nil {
		return nil, err
	}
	cfg.Pass = []byte(pass)
	return cfg, nil
}

// promptProxyScheme asks for the proxy protocol. TTY sessions get a
// huh select with a free-text slot; piped input gets the printed
// numbered menu with identical accepted inputs. It returns the index
// into http/socks5/https.
func promptProxyScheme(r io.Reader) (int, error) {
	if tui.Interactive() {
		idx, err := tui.Select("Loại proxy:", []string{
			"http",
			"socks5",
			"https",
			"tự nhập...",
		}, 0)
		if err != nil {
			return 0, err
		}
		if idx != 3 {
			return idx, nil
		}
		return readSchemeFreeText(r)
	}
	return promptProxySchemePiped(r)
}

// promptProxySchemePiped is the non-TTY fallback: the printed numbered
// menu, same accepted inputs as the huh select.
func promptProxySchemePiped(r io.Reader) (int, error) {
	printNumberedMenu("Loại proxy:", []string{
		"http   (mặc định, Enter)",
		"socks5",
		"https  (CONNECT qua TLS)",
		"tự nhập...",
	}, "Chọn (1-4, Enter = http): ")
	choice := readMenuLine(r, "1")
	switch choice {
	case "1":
		return 0, nil
	case "2":
		return 1, nil
	case "3":
		return 2, nil
	}
	if idx, ok := normalizeProxyScheme(choice); ok {
		return idx, nil
	}
	return readSchemeFreeText(r)
}

// readSchemeFreeText reads a freely typed scheme name until it names
// a supported protocol. A closed reader aborts instead of hanging.
// validateProxyScheme rejects anything but the supported protocols.
// Extracted pure so tests pin it without a TTY.
func validateProxyScheme(s string) error {
	if _, ok := normalizeProxyScheme(s); !ok {
		return errors.New("chỉ hỗ trợ http, socks5, https")
	}
	return nil
}

// readSchemeFreeText reads a freely typed scheme name. TTY sessions
// get a huh input with inline validation; piped input loops the
// plain prompt. A closed reader aborts instead of hanging.
func readSchemeFreeText(r io.Reader) (int, error) {
	if tui.Interactive() {
		var typed string
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Nhập loại proxy").Value(&typed).Validate(validateProxyScheme),
		))
		if err := form.Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return 0, tui.ErrAborted
			}
			return 0, err
		}
		idx, _ := normalizeProxyScheme(typed)
		return idx, nil
	}
	for {
		fmt.Print("Nhập loại proxy (http/socks5/https): ")
		typed, err := readLineRaw(r)
		if err != nil && len(typed) == 0 {
			return 0, err
		}
		if idx, ok := normalizeProxyScheme(typed); ok {
			return idx, nil
		}
		fmt.Println("❌ Chỉ hỗ trợ http, socks5, https — nhập lại.")
	}
}

// normalizeProxyScheme lowercases and trims input, accepting the menu
// numbers 1-3 as well as the scheme names themselves. It returns the
// index into http/socks5/https.
func normalizeProxyScheme(s string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "http":
		return 0, true
	case "2", "socks5":
		return 1, true
	case "3", "https":
		return 2, true
	default:
		return 0, false
	}
}

// printNumberedMenu renders a title, 1-based options, and a prompt
// line with no input handling. Pure print, so callers own reading.
func printNumberedMenu(title string, options []string, prompt string) {
	fmt.Println(title)
	for i, opt := range options {
		fmt.Printf("  [%d] %s\n", i+1, opt)
	}
	fmt.Print(prompt)
}

// readMenuLine reads one raw line and trims it. Empty input or a read
// error yields def, keeping menus deterministic on closed pipes.
func readMenuLine(r io.Reader, def string) string {
	line, err := readLineRaw(r)
	if err != nil && len(line) == 0 {
		return def
	}
	if strings.TrimSpace(line) == "" {
		return def
	}
	return strings.TrimSpace(line)
}
