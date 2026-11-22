// Package handoff defines the port for documents that agents exchange outside
// their worktrees. The file-backed implementation lives in
// internal/infra/sharedfolder because the capability gate keeps file calls in
// infra.
package handoff

import (
	"context"

	infra "github.com/refusedguy/beacon/internal/infra/sharedfolder"
)

// Kind, Key and Document are aliases of the infrastructure types so callers
// only need this package.
type Kind = infra.Kind
type Key = infra.Key
type Document = infra.Document

const (
	KindPlan        = infra.KindPlan
	KindNote        = infra.KindNote
	KindFinding     = infra.KindFinding
	KindHandoff     = infra.KindHandoff
	KindDeliverable = infra.KindDeliverable
)

var (
	ErrInvalidKey = infra.ErrInvalidKey
	ErrNotFound   = infra.ErrNotFound
)

// InvalidKeyError names the field that failed validation.
type InvalidKeyError = infra.InvalidKeyError

// SharedFolder is the port agents and the orchestrator use to exchange
// documents. Implementations must be safe for concurrent use by many processes.
type SharedFolder interface {
	Put(ctx context.Context, key Key, body []byte) (Document, error)
	Get(ctx context.Context, key Key) (Document, error)
	List(ctx context.Context, workstreamID string, kind Kind) ([]Document, error)
}

var _ SharedFolder = (*infra.Store)(nil)

// NewFileSharedFolder returns the file-backed implementation of SharedFolder.
// The root must be an absolute state directory outside agent worktrees.
func NewFileSharedFolder(root string) (*infra.Store, error) {
	return infra.NewFileStore(root)
}
