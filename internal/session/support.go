package session

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/dunea/dujiao-next-telegram-bot/internal/support"
)

type supportStore struct{ r *Redis }

func (r *Redis) SupportStore() support.Store  { return &supportStore{r: r} }
func (m *Memory) SupportStore() support.Store { return support.NewMemory() }
func (s *supportStore) read(ctx context.Context, key string) (support.Record, string, error) {
	if !support.ValidKey(key) {
		return support.Record{}, "", support.ErrInvalid
	}
	values, err := s.r.client.Eval(ctx, receiptRead, []string{s.r.prefix + ":epoch", s.r.prefix + ":support:v1:" + key}, s.r.epoch).Slice()
	if err != nil || len(values) != 1 {
		return support.Record{}, "", support.ErrUnavailable
	}
	body, ok := values[0].(string)
	if !ok {
		return support.Record{}, "", support.ErrUnavailable
	}
	if body == "" {
		return support.Record{}, "", nil
	}
	var r support.Record
	if json.Unmarshal([]byte(body), &r) != nil || !support.Valid(key, r, s.r.now()) {
		return support.Record{}, "", support.ErrInvalid
	}
	return r, body, nil
}
func (s *supportStore) Get(ctx context.Context, key string) (support.Record, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, body, err := s.read(ctx, key)
	return r, body != "", err
}
func (s *supportStore) Update(ctx context.Context, key string, fn func(*support.Record) error) (support.Record, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, body, err := s.read(ctx, key)
	if err != nil {
		return support.Record{}, err
	}
	original := r
	if err = fn(&r); err != nil {
		return support.Record{}, err
	}
	if !support.Valid(key, r, s.r.now()) {
		return support.Record{}, support.ErrInvalid
	}
	if body != "" && reflect.DeepEqual(original, r) {
		return r, nil
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return support.Record{}, support.ErrInvalid
	}
	ttl := r.ExpiresAt.Sub(s.r.now()).Milliseconds()
	if ttl <= 0 || ttl > support.MessageLifetime.Milliseconds() {
		return support.Record{}, support.ErrInvalid
	}
	keys := []string{s.r.prefix + ":epoch", s.r.prefix + ":support:v1:" + key, s.r.prefix + ":support:index:v1"}
	result, err := s.r.client.Eval(ctx, receiptCAS, keys, s.r.epoch, body, key, encoded, ttl, support.Capacity, support.MessageLifetime.Milliseconds()).Int()
	if err != nil {
		return support.Record{}, support.ErrUnavailable
	}
	switch result {
	case 1:
		return r, nil
	case 0:
		return support.Record{}, support.ErrConflict
	case -2:
		return support.Record{}, support.ErrCapacity
	default:
		return support.Record{}, support.ErrUnavailable
	}
}
