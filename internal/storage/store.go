package storage

import "context"

type Store interface {
	SaveSession(ctx context.Context, sessionID string, data []byte) error
	LoadSession(ctx context.Context, sessionID string) ([]byte, error)
	ListSessionIDs(ctx context.Context, limit int) ([]string, error)
	SaveApproval(ctx context.Context, approvalID string, data []byte) error
	LoadApproval(ctx context.Context, approvalID string) ([]byte, error)
}
