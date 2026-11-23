package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type connectionInfo struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func acquireLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "beacon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("beacon is already running: %w", err)
	}
	return f, nil
}

func writeConnection(dir, addr string) (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	info := connectionInfo{Addr: addr, Token: token, PID: os.Getpid()}
	data, err := json.Marshal(info)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "connection.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func readConnection(dir string) (*connectionInfo, error) {
	b, err := os.ReadFile(filepath.Join(dir, "connection.json"))
	if err != nil {
		return nil, err
	}
	var info connectionInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func removeConnection(dir string) {
	_ = os.Remove(filepath.Join(dir, "connection.json"))
}
