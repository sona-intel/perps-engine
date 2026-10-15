package sources

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"perps/internal/config"
	"perps/internal/perpl"
)

const (
	defaultMaxLogs   = 1000
	hyperSyncTimeout = 120 * time.Second
)

var ErrNoKey = errors.New("hypersync key is not set")

// Attempts counts the first request too
type Backoff struct {
	Initial  time.Duration
	Max      time.Duration
	Attempts int
	Sleep    func(ctx context.Context, d time.Duration) error
}

func DefaultBackoff() Backoff {
	return Backoff{Initial: 8 * time.Second, Max: 60 * time.Second, Attempts: 8, Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Retry-After wins over the exponential delay when it is longer and Max caps both
func (b Backoff) wait(retry int, after time.Duration) time.Duration {
	d := b.Initial
	for range retry {
		d *= 2
		if d >= b.Max {
			break
		}
	}
	return min(max(d, after), b.Max)
}

// To is exclusive like the API's to_block
type Query struct {
	Address perpl.Address
	Topic0  perpl.Hash
	From    uint64
	To      uint64
	MaxLogs int
}

type Log struct {
	Address        perpl.Address
	Topics         []perpl.Hash
	Data           []byte
	Block          uint64
	LogIndex       uint64
	TxHash         perpl.Hash
	BlockTimestamp uint64
	From           perpl.Address
}

// NextBlock is the first block the response did not cover
type Page struct {
	Logs          []Log
	NextBlock     uint64
	ArchiveHeight uint64
}

type HyperSync struct {
	url     string
	key     config.Secret
	hc      *http.Client
	backoff Backoff
	mu      sync.Mutex
}

type HyperSyncOption func(*HyperSync)

func WithHyperSyncHTTPClient(hc *http.Client) HyperSyncOption {
	return func(h *HyperSync) { h.hc = hc }
}

func WithBackoff(b Backoff) HyperSyncOption {
	return func(h *HyperSync) {
		if b.Sleep == nil {
			b.Sleep = sleep
		}
		h.backoff = b
	}
}

func NewHyperSync(queryURL string, key config.Secret, opts ...HyperSyncOption) *HyperSync {
	h := &HyperSync{url: queryURL, key: key, hc: newHTTPClient(hyperSyncTimeout), backoff: DefaultBackoff()}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Without these fmt prints the private key field raw because it cannot call methods on it
func (h *HyperSync) String() string { return "hypersync(" + h.url + ")" }

func (h *HyperSync) GoString() string { return h.String() }

/* Requests */

type hsRequest struct {
	FromBlock      uint64        `json:"from_block"`
	ToBlock        uint64        `json:"to_block"`
	Logs           []hsLogFilter `json:"logs"`
	FieldSelection hsFields      `json:"field_selection"`
	MaxNumLogs     int           `json:"max_num_logs"`
}

type hsLogFilter struct {
	Address []string   `json:"address"`
	Topics  [][]string `json:"topics"`
}

type hsFields struct {
	Log         []string `json:"log"`
	Block       []string `json:"block"`
	Transaction []string `json:"transaction"`
}

var fieldSelection = hsFields{
	Log:         []string{"block_number", "log_index", "transaction_index", "transaction_hash", "address", "data", "topic0"},
	Block:       []string{"number", "hash", "timestamp"},
	Transaction: []string{"block_number", "transaction_index", "hash", "from"},
}

func (q Query) body() ([]byte, error) {
	return json.Marshal(hsRequest{
		FromBlock:      q.From,
		ToBlock:        q.To,
		Logs:           []hsLogFilter{{Address: []string{q.Address.String()}, Topics: [][]string{{q.Topic0.String()}}}},
		FieldSelection: fieldSelection,
		MaxNumLogs:     cmp.Or(max(q.MaxLogs, 0), defaultMaxLogs),
	})
}

func (h *HyperSync) Page(ctx context.Context, q Query) (Page, error) {
	if !h.key.Set() {
		return Page{}, ErrNoKey
	}
	if q.From >= q.To {
		return Page{NextBlock: q.From}, nil
	}
	body, err := q.body()
	if err != nil {
		return Page{}, fmt.Errorf("hypersync request: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 1; ; attempt++ {
		raw, err := h.post(ctx, body)
		var se *StatusError
		if !errors.As(err, &se) || !retriable(se.Status) || attempt >= h.backoff.Attempts {
			if err != nil {
				return Page{}, err
			}
			return parsePage(raw)
		}
		if err := h.backoff.Sleep(ctx, h.backoff.wait(attempt-1, se.RetryAfter)); err != nil {
			return Page{}, err
		}
	}
}

func retriable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (h *HyperSync) post(ctx context.Context, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("hypersync request is malformed")
	}
	req.Header.Set("Authorization", "Bearer "+h.key.Reveal())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hypersync: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hypersync: %w", statusError(resp))
	}
	raw, err := readBody(resp.Body, maxHyperBody)
	if err != nil {
		return nil, fmt.Errorf("hypersync: %w", err)
	}
	return raw, nil
}

// Returns the first block fn has not handled, and stops early without an error when the archive has no more to give
func (h *HyperSync) Scan(ctx context.Context, q Query, fn func(Page) error) (uint64, error) {
	next := q.From
	for next < q.To {
		step := q
		step.From = next
		page, err := h.Page(ctx, step)
		if err != nil {
			return next, err
		}
		if err := fn(page); err != nil {
			return next, err
		}
		if page.NextBlock <= next {
			return next, nil
		}
		next = min(page.NextBlock, q.To)
	}
	return next, nil
}

/* Responses */

type hsResponse struct {
	Data []struct {
		Logs         []hsLog   `json:"logs"`
		Blocks       []hsBlock `json:"blocks"`
		Transactions []hsTx    `json:"transactions"`
	} `json:"data"`
	ArchiveHeight *quantity `json:"archive_height"`
	NextBlock     *quantity `json:"next_block"`
}

type hsLog struct {
	BlockNumber *quantity `json:"block_number"`
	LogIndex    *quantity `json:"log_index"`
	TxHash      string    `json:"transaction_hash"`
	Address     string    `json:"address"`
	Data        string    `json:"data"`
	Topic0      *string   `json:"topic0"`
	Topic1      *string   `json:"topic1"`
	Topic2      *string   `json:"topic2"`
	Topic3      *string   `json:"topic3"`
}

type hsBlock struct {
	Number    *quantity `json:"number"`
	Timestamp *quantity `json:"timestamp"`
}

type hsTx struct {
	Hash string `json:"hash"`
	From string `json:"from"`
}

func parsePage(raw []byte) (Page, error) {
	var resp hsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Page{}, fmt.Errorf("hypersync: decode response: %w", err)
	}
	if resp.NextBlock == nil {
		return Page{}, errors.New("hypersync: response has no next_block")
	}
	stamps := map[uint64]uint64{}
	senders := map[perpl.Hash]perpl.Address{}
	for _, item := range resp.Data {
		for _, b := range item.Blocks {
			if b.Number != nil && b.Timestamp != nil {
				stamps[uint64(*b.Number)] = uint64(*b.Timestamp)
			}
		}
		for _, tx := range item.Transactions {
			hash, errH := perpl.ParseHash(tx.Hash)
			from, errF := perpl.ParseAddress(tx.From)
			if err := errors.Join(errH, errF); err != nil {
				return Page{}, fmt.Errorf("hypersync: bad transaction entry: %w", err)
			}
			senders[hash] = from
		}
	}
	page := Page{NextBlock: uint64(*resp.NextBlock)}
	if resp.ArchiveHeight != nil {
		page.ArchiveHeight = uint64(*resp.ArchiveHeight)
	}
	for _, item := range resp.Data {
		for _, l := range item.Logs {
			log, err := l.log(stamps, senders)
			if err != nil {
				return Page{}, fmt.Errorf("hypersync: %w", err)
			}
			page.Logs = append(page.Logs, log)
		}
	}
	slices.SortFunc(page.Logs, func(a, b Log) int {
		return cmp.Or(cmp.Compare(a.Block, b.Block), cmp.Compare(a.LogIndex, b.LogIndex))
	})
	return page, nil
}

func (l hsLog) log(stamps map[uint64]uint64, senders map[perpl.Hash]perpl.Address) (Log, error) {
	if l.BlockNumber == nil || l.LogIndex == nil || l.Topic0 == nil {
		return Log{}, errors.New("log lacks block_number, log_index or topic0")
	}
	out := Log{Block: uint64(*l.BlockNumber), LogIndex: uint64(*l.LogIndex)}
	var err error
	if out.TxHash, err = perpl.ParseHash(l.TxHash); err != nil {
		return Log{}, fmt.Errorf("log %d/%d: transaction_hash: %w", out.Block, out.LogIndex, err)
	}
	if out.Address, err = perpl.ParseAddress(l.Address); err != nil {
		return Log{}, fmt.Errorf("log %d/%d: address: %w", out.Block, out.LogIndex, err)
	}
	digits, ok := strings.CutPrefix(l.Data, "0x")
	if !ok {
		return Log{}, fmt.Errorf("log %d/%d: data has no 0x prefix", out.Block, out.LogIndex)
	}
	if out.Data, err = hex.DecodeString(digits); err != nil {
		return Log{}, fmt.Errorf("log %d/%d: data is not hex", out.Block, out.LogIndex)
	}
	for _, t := range []*string{l.Topic0, l.Topic1, l.Topic2, l.Topic3} {
		if t == nil {
			break
		}
		topic, err := perpl.ParseHash(*t)
		if err != nil {
			return Log{}, fmt.Errorf("log %d/%d: topic: %w", out.Block, out.LogIndex, err)
		}
		out.Topics = append(out.Topics, topic)
	}
	ts, ok := stamps[out.Block]
	if !ok {
		return Log{}, fmt.Errorf("log %d/%d: no timestamp for block %d", out.Block, out.LogIndex, out.Block)
	}
	from, ok := senders[out.TxHash]
	if !ok {
		return Log{}, fmt.Errorf("log %d/%d: no sender for transaction %s", out.Block, out.LogIndex, out.TxHash)
	}
	out.BlockTimestamp, out.From = ts, from
	return out, nil
}
