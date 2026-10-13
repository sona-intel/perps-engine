package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"perps/internal/perpl"
)

type Block struct {
	Number uint64
	Hash   perpl.Hash
	Time   time.Time
}

type Tx struct {
	Hash perpl.Hash
	From perpl.Address
}

type Log struct {
	Address        perpl.Address
	Topics         []perpl.Hash
	Data           []byte
	Block          uint64
	LogIndex       uint64
	TxHash         perpl.Hash
	BlockTimestamp uint64
	Removed        bool
}

/* Hex quantities */

func quantity(n uint64) string { return "0x" + strconv.FormatUint(n, 16) }

func parseQuantity(s string) (uint64, error) {
	digits, ok := strings.CutPrefix(s, "0x")
	if !ok || digits == "" {
		return 0, fmt.Errorf("bad quantity %q", s)
	}
	n, err := strconv.ParseUint(digits, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("bad quantity %q", s)
	}
	return n, nil
}

func decodeInto(raw []byte, out any) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("unexpected result shape")
	}
	return nil
}

/* Methods */

func (c *Client) Call(ctx context.Context, to perpl.Address, data []byte, block uint64) ([]byte, error) {
	msg := map[string]string{"to": to.String(), "data": "0x" + hex.EncodeToString(data)}
	raw, err := c.call(ctx, "eth_call", msg, quantity(block))
	if err != nil {
		return nil, err
	}
	var s string
	if err := decodeInto(raw, &s); err != nil {
		return nil, fmt.Errorf("rpc eth_call: %w", err)
	}
	out, err := decodeHex(s)
	if err != nil {
		return nil, fmt.Errorf("rpc eth_call: %w", err)
	}
	return out, nil
}

func decodeHex(s string) ([]byte, error) {
	body, ok := strings.CutPrefix(s, "0x")
	if !ok {
		return nil, errors.New("hex data has no 0x prefix")
	}
	out, err := hex.DecodeString(body)
	if err != nil {
		return nil, errors.New("hex data is malformed")
	}
	return out, nil
}

func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	raw, err := c.call(ctx, "eth_blockNumber")
	if err != nil {
		return 0, err
	}
	return quantityResult(raw, "eth_blockNumber")
}

func quantityResult(raw []byte, method string) (uint64, error) {
	var s string
	if err := decodeInto(raw, &s); err != nil {
		return 0, fmt.Errorf("rpc %s: %w", method, err)
	}
	n, err := parseQuantity(s)
	if err != nil {
		return 0, fmt.Errorf("rpc %s: %w", method, err)
	}
	return n, nil
}

type wireBlock struct {
	Number    string `json:"number"`
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp"`
}

func (c *Client) BlockByNumber(ctx context.Context, number uint64) (Block, error) {
	raw, err := c.call(ctx, "eth_getBlockByNumber", quantity(number), false)
	if err != nil {
		return Block{}, err
	}
	if isNull(raw) {
		return Block{}, fmt.Errorf("rpc eth_getBlockByNumber: block %d: %w", number, ErrNotFound)
	}
	var w wireBlock
	if err := decodeInto(raw, &w); err != nil {
		return Block{}, fmt.Errorf("rpc eth_getBlockByNumber: %w", err)
	}
	got, err := parseQuantity(w.Number)
	if err != nil {
		return Block{}, fmt.Errorf("rpc eth_getBlockByNumber: %w", err)
	}
	if got != number {
		return Block{}, fmt.Errorf("rpc eth_getBlockByNumber: node returned block %d for %d", got, number)
	}
	hash, err := perpl.ParseHash(w.Hash)
	if err != nil {
		return Block{}, fmt.Errorf("rpc eth_getBlockByNumber: %w", err)
	}
	stamp, err := parseQuantity(w.Timestamp)
	if err != nil {
		return Block{}, fmt.Errorf("rpc eth_getBlockByNumber: %w", err)
	}
	return Block{Number: got, Hash: hash, Time: time.Unix(int64(stamp), 0).UTC()}, nil
}

func isNull(raw []byte) bool { return string(raw) == "null" }

func (c *Client) StorageAt(ctx context.Context, addr perpl.Address, slot perpl.Hash, block uint64) (perpl.Hash, error) {
	raw, err := c.call(ctx, "eth_getStorageAt", addr.String(), slot.String(), quantity(block))
	if err != nil {
		return perpl.Hash{}, err
	}
	var s string
	if err := decodeInto(raw, &s); err != nil {
		return perpl.Hash{}, fmt.Errorf("rpc eth_getStorageAt: %w", err)
	}
	out, err := perpl.ParseHash(s)
	if err != nil {
		return perpl.Hash{}, fmt.Errorf("rpc eth_getStorageAt: %w", err)
	}
	return out, nil
}

type wireTx struct {
	Hash string `json:"hash"`
	From string `json:"from"`
}

func (c *Client) TransactionByHash(ctx context.Context, hash perpl.Hash) (Tx, error) {
	raw, err := c.call(ctx, "eth_getTransactionByHash", hash.String())
	if err != nil {
		return Tx{}, err
	}
	if isNull(raw) {
		return Tx{}, fmt.Errorf("rpc eth_getTransactionByHash: %s: %w", hash, ErrNotFound)
	}
	var w wireTx
	if err := decodeInto(raw, &w); err != nil {
		return Tx{}, fmt.Errorf("rpc eth_getTransactionByHash: %w", err)
	}
	got, err := perpl.ParseHash(w.Hash)
	if err != nil {
		return Tx{}, fmt.Errorf("rpc eth_getTransactionByHash: %w", err)
	}
	if got != hash {
		return Tx{}, errors.New("rpc eth_getTransactionByHash: node returned a different transaction")
	}
	from, err := perpl.ParseAddress(w.From)
	if err != nil {
		return Tx{}, fmt.Errorf("rpc eth_getTransactionByHash: %w", err)
	}
	return Tx{Hash: got, From: from}, nil
}

type wireLog struct {
	Address        string   `json:"address"`
	Topics         []string `json:"topics"`
	Data           string   `json:"data"`
	BlockNumber    string   `json:"blockNumber"`
	LogIndex       string   `json:"logIndex"`
	TxHash         string   `json:"transactionHash"`
	BlockTimestamp string   `json:"blockTimestamp"`
	Removed        bool     `json:"removed"`
}

func (w wireLog) convert() (Log, error) {
	var (
		l   Log
		err error
	)
	if l.Address, err = perpl.ParseAddress(w.Address); err != nil {
		return Log{}, err
	}
	for _, t := range w.Topics {
		topic, err := perpl.ParseHash(t)
		if err != nil {
			return Log{}, err
		}
		l.Topics = append(l.Topics, topic)
	}
	if l.Data, err = decodeHex(w.Data); err != nil {
		return Log{}, err
	}
	if l.Block, err = parseQuantity(w.BlockNumber); err != nil {
		return Log{}, err
	}
	if l.LogIndex, err = parseQuantity(w.LogIndex); err != nil {
		return Log{}, err
	}
	if l.TxHash, err = perpl.ParseHash(w.TxHash); err != nil {
		return Log{}, err
	}
	if w.BlockTimestamp != "" {
		if l.BlockTimestamp, err = parseQuantity(w.BlockTimestamp); err != nil {
			return Log{}, err
		}
	}
	l.Removed = w.Removed
	return l, nil
}

// Logs pages in windows of at most MaxLogSpan blocks and halves a window the node rejects with CodeRangeTooLarge
func (c *Client) Logs(ctx context.Context, addr perpl.Address, topic0 perpl.Hash, from, to uint64) ([]Log, error) {
	if from > to {
		return nil, fmt.Errorf("rpc eth_getLogs: from block %d is after to block %d", from, to)
	}
	logs := []Log{}
	span := uint64(MaxLogSpan)
	for next := from; ; {
		end := to
		if to-next >= span {
			end = next + span - 1
		}
		page, err := c.logWindow(ctx, addr, topic0, next, end)
		var rpcErr *Error
		if errors.As(err, &rpcErr) && rpcErr.Code == CodeRangeTooLarge && end > next {
			span = max(1, (end-next+1)/2)
			continue
		}
		if err != nil {
			return nil, err
		}
		logs = append(logs, page...)
		if end == to {
			return logs, nil
		}
		next = end + 1
	}
}

func (c *Client) logWindow(ctx context.Context, addr perpl.Address, topic0 perpl.Hash, from, to uint64) ([]Log, error) {
	filter := map[string]any{
		"fromBlock": quantity(from),
		"toBlock":   quantity(to),
		"address":   addr.String(),
		"topics":    []string{topic0.String()},
	}
	raw, err := c.call(ctx, "eth_getLogs", filter)
	if err != nil {
		return nil, err
	}
	var wire []wireLog
	if err := decodeInto(raw, &wire); err != nil {
		return nil, fmt.Errorf("rpc eth_getLogs: %w", err)
	}
	out := make([]Log, 0, len(wire))
	for i, w := range wire {
		l, err := w.convert()
		if err != nil {
			return nil, fmt.Errorf("rpc eth_getLogs: log %d: %w", i, err)
		}
		out = append(out, l)
	}
	return out, nil
}
