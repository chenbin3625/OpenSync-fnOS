package native

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Capture the server key before sending any credentials. Subsequent rclone
// connections, including each scheduled run, verify the saved host key.
func firstHostKey(ctx context.Context, host, port string) (string, error) {
	if host == "" || strings.ContainsAny(host, "/\\ \t\r\n") {
		return "", errors.New("SFTP 主机无效")
	}
	if port == "" {
		port = "22"
	}
	addr := net.JoinHostPort(strings.Trim(host, "[]"), port)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := new(net.Dialer).DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", errors.New("无法获取 SFTP 主机公钥，请检查地址或提供 known_hosts 公钥")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var line string
	capture := errors.New("host key captured")
	_, _, _, _ = ssh.NewClientConn(conn, addr, &ssh.ClientConfig{User: "opensync", HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		line = knownhosts.Line([]string{addr}, key)
		return capture
	}})
	if line == "" {
		return "", errors.New("未取得 SFTP 主机公钥")
	}
	return line, nil
}
