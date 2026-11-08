// Package support stores customer routing metadata, never message content.
package support

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ModeLifetime    = 24 * time.Hour
	MessageLifetime = 7 * 24 * time.Hour
	Capacity        = 50000
)

var (
	ErrUnavailable = errors.New("support storage unavailable")
	ErrConflict    = errors.New("support record changed")
	ErrCapacity    = errors.New("support storage capacity reached")
	ErrInvalid     = errors.New("invalid support record")
	ErrDuplicate   = errors.New("support message already handled or pending")
)

type Record struct {
	Kind              string
	UserID            int64
	SourceMessageID   int
	OrderID           uint
	Status            string
	Token             string
	TelegramMessageID int
	ExpiresAt         time.Time
}

type Store interface {
	Get(context.Context, string) (Record, bool, error)
	Update(context.Context, string, func(*Record) error) (Record, error)
}

func ModeKey(uid int64) string { return "mode:" + strconv.FormatInt(uid, 10) }
func RouteKey(chat int64, mid int) string {
	return "route:" + strconv.FormatInt(chat, 10) + ":" + strconv.Itoa(mid)
}
func DeliveryKey(direction string, chat int64, mid int) string {
	return "delivery:" + direction + ":" + strconv.FormatInt(chat, 10) + ":" + strconv.Itoa(mid)
}
func ValidKey(key string) bool {
	p := strings.Split(key, ":")
	positive := func(s string) bool {
		n, e := strconv.ParseInt(s, 10, 64)
		return e == nil && n > 0 && strconv.FormatInt(n, 10) == s
	}
	negative := func(s string) bool {
		n, e := strconv.ParseInt(s, 10, 64)
		return e == nil && n < 0 && strconv.FormatInt(n, 10) == s
	}
	if len(p) == 2 && p[0] == "mode" {
		return positive(p[1])
	}
	if len(p) == 3 && p[0] == "route" {
		return negative(p[1]) && positive(p[2])
	}
	if len(p) == 4 && p[0] == "delivery" {
		return (p[1] == "customer" && positive(p[2]) || p[1] == "staff" && negative(p[2])) && positive(p[3])
	}
	return false
}
func Valid(key string, r Record, now time.Time) bool {
	if !ValidKey(key) || r.UserID <= 0 || !now.Before(r.ExpiresAt) || r.ExpiresAt.After(now.Add(MessageLifetime)) {
		return false
	}
	switch r.Kind {
	case "mode":
		return key == ModeKey(r.UserID) && (r.Status == "active" || r.Status == "closed") && !r.ExpiresAt.After(now.Add(ModeLifetime))
	case "route":
		return strings.HasPrefix(key, "route:") && r.SourceMessageID > 0
	case "delivery":
		parts := strings.Split(key, ":")
		if !strings.HasPrefix(key, "delivery:") || r.Token == "" || r.SourceMessageID <= 0 || parts[len(parts)-1] != strconv.Itoa(r.SourceMessageID) || (parts[1] == "customer" && parts[2] != strconv.FormatInt(r.UserID, 10)) {
			return false
		}
		switch r.Status {
		case "posting", "sent", "failed", "unknown":
			return r.Status != "sent" || r.TelegramMessageID > 0
		}
	}
	return false
}

type Memory struct {
	mu     sync.Mutex
	values map[string]Record
	now    func() time.Time
}

func NewMemory() *Memory { return &Memory{values: make(map[string]Record), now: time.Now} }
func (m *Memory) Get(ctx context.Context, key string) (Record, bool, error) {
	if ctx.Err() != nil {
		return Record{}, false, ErrUnavailable
	}
	if !ValidKey(key) {
		return Record{}, false, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.values[key]
	if ok && !m.now().Before(r.ExpiresAt) {
		delete(m.values, key)
		return Record{}, false, nil
	}
	if ok && !Valid(key, r, m.now()) {
		return Record{}, false, ErrInvalid
	}
	return r, ok, nil
}
func (m *Memory) Update(ctx context.Context, key string, fn func(*Record) error) (Record, error) {
	if ctx.Err() != nil {
		return Record{}, ErrUnavailable
	}
	if !ValidKey(key) {
		return Record{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	r, ok := m.values[key]
	if ok && !now.Before(r.ExpiresAt) {
		delete(m.values, key)
		r = Record{}
		ok = false
	}
	if !ok && len(m.values) >= Capacity {
		for k, v := range m.values {
			if !now.Before(v.ExpiresAt) {
				delete(m.values, k)
			}
		}
		if len(m.values) >= Capacity {
			return Record{}, ErrCapacity
		}
	}
	if err := fn(&r); err != nil {
		return Record{}, err
	}
	if !Valid(key, r, m.now()) {
		return Record{}, ErrInvalid
	}
	m.values[key] = r
	return r, nil
}
