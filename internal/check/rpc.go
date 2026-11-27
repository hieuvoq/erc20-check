package check

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
var addressRE = regexp.MustCompile("^0x[0-9a-fA-F]{40}$")
var hashRE = regexp.MustCompile("^0x[0-9a-fA-F]{64}$")

const zero = "0x0000000000000000000000000000000000000000"

var errNoResult = errors.New("no result")

type rpc struct{ url, socket string }

func (r rpc) call(ctx context.Context, method string, params []any, out any) error {
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	var reader io.Reader
	if r.socket != "" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", r.socket)
		if err != nil {
			return err
		}
		defer conn.Close()
		deadline := time.Now().Add(40 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		conn.SetDeadline(deadline)
		if err = json.NewEncoder(conn).Encode(req); err != nil {
			return err
		}
		reader = conn
	} else {
		// Only these read methods can ever reach the upstream.
		if method != "eth_chainId" && method != "eth_getBlockByNumber" {
			return errors.New("method requires private fork")
		}
		b, err := json.Marshal(req)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, "POST", r.url, bytes.NewReader(b))
		if err != nil {
			return errors.New("invalid RPC URL")
		}
		request.Header.Set("Content-Type", "application/json")
		res, err := httpClient.Do(request)
		if err != nil {
			return fmt.Errorf("%s: upstream unavailable", method)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("%s: upstream HTTP %d", method, res.StatusCode)
		}
		reader = res.Body
	}
	var reply struct {
		Result json.RawMessage
		Error  *struct {
			Code    int
			Message string
		}
	}
	if err := json.NewDecoder(io.LimitReader(reader, 8<<20)).Decode(&reply); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if reply.Error != nil {
		return fmt.Errorf("%s: RPC %d: %s", method, reply.Error.Code, strings.ReplaceAll(reply.Error.Message, r.url, "[RPC]"))
	}
	if len(reply.Result) == 0 || string(reply.Result) == "null" {
		return fmt.Errorf("%s: %w", method, errNoResult)
	}
	return json.Unmarshal(reply.Result, out)
}
func number(s string) (*big.Int, error) {
	if !strings.HasPrefix(s, "0x") {
		return nil, errors.New("expected hex quantity")
	}
	v, ok := new(big.Int).SetString(s[2:], 16)
	if !ok || v.Sign() < 0 {
		return nil, errors.New("invalid hex quantity")
	}
	return v, nil
}
func scalar(ctx context.Context, r rpc, m string, params ...any) (string, error) {
	var s string
	if params == nil {
		params = []any{}
	}
	err := r.call(ctx, m, params, &s)
	return s, err
}

type header struct{ Number, Hash string }

func block(ctx context.Context, r rpc, tag string) (header, error) {
	var b header
	err := r.call(ctx, "eth_getBlockByNumber", []any{tag, false}, &b)
	if err == nil && !hashRE.MatchString(b.Hash) {
		err = errors.New("invalid block hash")
	}
	return b, err
}

// Fork owns the child process and a private IPC socket. No signing keys are needed.
func fork(ctx context.Context, upstream, blockHex string) (rpc, func(), error) {
	height, err := number(blockHex)
	if err != nil {
		return rpc{}, nil, err
	}
	dir, err := os.MkdirTemp("", "erc20-fork-")
	if err != nil {
		return rpc{}, nil, err
	}
	path := filepath.Join(dir, "rpc")
	cmd := exec.CommandContext(ctx, "anvil", "--host", "127.0.0.1", "--port", "0", "--ipc", path, "--mnemonic-random", "12", "--accounts", "2", "--fork-url", upstream, "--fork-block-number", height.String(), "--no-storage-caching", "--silent")
	if err = cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return rpc{}, nil, fmt.Errorf("start anvil: %w", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	cleanup := func() { _ = cmd.Process.Kill(); <-done; os.RemoveAll(dir) }
	// Unix socket paths are short on macOS; the CLI permits TMPDIR=/tmp.
	timer := time.NewTimer(40 * time.Second)
	defer timer.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return rpc{url: upstream, socket: path}, cleanup, nil
		}
		select {
		case <-ctx.Done():
			cleanup()
			return rpc{}, nil, ctx.Err()
		case <-done:
			cleanup()
			return rpc{}, nil, errors.New("anvil exited during fork startup; check archive RPC, Foundry version and TMPDIR")
		case <-timer.C:
			cleanup()
			return rpc{}, nil, errors.New("fork startup timed out")
		case <-time.After(80 * time.Millisecond):
		}
	}
}
func ValidURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}
