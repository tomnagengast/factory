package objectstore

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("object not found")

type Store interface {
	Put(context.Context, string, []byte, string) error
	Get(context.Context, string) ([]byte, error)
	List(context.Context, string) ([]string, error)
}
