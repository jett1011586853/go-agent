package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var ErrNotFound = errors.New("storage: not found")

const (
	bucketSessions  = "sessions"
	bucketApprovals = "approvals"
)

type BoltStore struct {
	db *bolt.DB
}

func NewBoltStore(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bolt db: %w", err)
	}
	s := &BoltStore{db: db}
	if err := s.ensureBuckets(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *BoltStore) ensureBuckets() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(bucketSessions)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(bucketApprovals)); err != nil {
			return err
		}
		return nil
	})
}

func (s *BoltStore) Close() error {
	return s.db.Close()
}

func (s *BoltStore) SaveSession(_ context.Context, sessionID string, data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucketSessions)).Put([]byte(sessionID), data)
	})
}

func (s *BoltStore) LoadSession(_ context.Context, sessionID string) ([]byte, error) {
	var out []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(bucketSessions)).Get([]byte(sessionID))
		if v == nil {
			return ErrNotFound
		}
		out = append([]byte(nil), v...)
		return nil
	})
	return out, err
}

func (s *BoltStore) ListSessionIDs(_ context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	out := make([]string, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(bucketSessions)).Cursor()
		for k, _ := c.Last(); k != nil && len(out) < limit; k, _ = c.Prev() {
			out = append(out, string(k))
		}
		return nil
	})
	return out, err
}

func (s *BoltStore) SaveApproval(_ context.Context, approvalID string, data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucketApprovals)).Put([]byte(approvalID), data)
	})
}

func (s *BoltStore) LoadApproval(_ context.Context, approvalID string) ([]byte, error) {
	var out []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(bucketApprovals)).Get([]byte(approvalID))
		if v == nil {
			return ErrNotFound
		}
		out = append([]byte(nil), v...)
		return nil
	})
	return out, err
}
