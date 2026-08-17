// Пакет sshtest — ПОДСТАВНАЯ МАШИНА С SSH: та, на которую контроллер заходит паролем.
//
// ┌─────────────────────────────────────────────────────────────────────────────────────┐
// │ ЛЕЖИТ В ОБЫЧНОМ ФАЙЛЕ, А НЕ В `_test.go`, НАМЕРЕННО — тот же довод, что у            │
// │ `internal/run/fake.go`: им пользуются тесты ДРУГИХ пакетов (ручки) и ПРОБА зоны,     │
// │ которая поднимает настоящий бинарь и настоящую машину рядом с ним. Из тестового      │
// │ файла ни то, ни другое взять нельзя.                                                 │
// │                                                                                      │
// │ В выпуск он не едет: на него не смотрит ни один путь из `cmd/control`.                │
// └─────────────────────────────────────────────────────────────────────────────────────┘
//
// Машина НЕ ПРИТВОРЯЕТСЯ настоящей: она принимает ОДИН пароль и выполняет присланную
// команду обычным шеллом в своём временном домашнем каталоге. Именно это и нужно
// проверить — что контроллер кладёт ключ в `~/.ssh/authorized_keys`, кладёт его ОДИН раз
// и умеет отличить «записалось» от «отчитались успехом».
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Machine — подставная машина. Дом у неё свой и временный: всё, что контроллер там
// напишет, лежит в нём и читается пробой как есть.
type Machine struct {
	Addr string
	User string
	Pass string
	Home string

	// Silent — машина выполняет команду, но НИЧЕГО не отвечает и выходит нулём. Так
	// выглядит «команда прошла, а измерить нечего», и различать это с успехом обязан
	// контроллер, а не мы за него.
	Silent bool

	listener net.Listener
	mu       sync.Mutex
	tried    []string
	closed   bool
}

// Start поднимает машину на случайном порту петли.
func Start(user, pass, home string) (*Machine, error) {
	return StartOn("127.0.0.1:0", user, pass, home)
}

// StartOn — то же на названном адресе: пробе порт нужен известным заранее.
func StartOn(addr, user, pass, home string) (*Machine, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	hostKey, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}

	m := &Machine{User: user, Pass: pass, Home: home}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, given []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(given) == pass {
				return nil, nil
			}
			return nil, errors.New("пароль не подошёл")
		},
	}
	cfg.AddHostKey(hostKey)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	m.listener = ln
	m.Addr = ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go m.serve(conn, cfg)
		}
	}()
	return m, nil
}

func (m *Machine) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	server, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "только сеанс")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go m.session(channel, requests)
	}
}

func (m *Machine) session(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for req := range requests {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)

		m.mu.Lock()
		m.tried = append(m.tried, payload.Command)
		m.mu.Unlock()

		// Команда выполняется НАСТОЯЩИМ шеллом в своём доме: проверяется то, что и правда
		// произойдёт на машине юзера, а не наш пересказ этой команды.
		cmd := exec.Command("/bin/sh", "-c", payload.Command)
		cmd.Env = append(os.Environ(), "HOME="+m.Home)
		out, err := cmd.CombinedOutput()
		if !m.Silent {
			_, _ = channel.Write(out)
		}

		code := 0
		if m.Silent {
			err = nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			code = 127
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
		return
	}
}

// Authorized — что лежит в `~/.ssh/authorized_keys` этой машины. Смотрим В ФАЙЛ, а не в
// ответ контроллера: он мог бы сказать что угодно.
func (m *Machine) Authorized() string {
	data, err := os.ReadFile(filepath.Join(m.Home, ".ssh", "authorized_keys"))
	if err != nil {
		return ""
	}
	return string(data)
}

// Commands — что на машине выполняли. Нужно ровно для одного: убедиться, что паролем
// ходили ОДИН раз и ничего сверх установки ключа не делали.
func (m *Machine) Commands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.tried))
	copy(out, m.tried)
	return out
}

func (m *Machine) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		_ = m.listener.Close()
	}
}
