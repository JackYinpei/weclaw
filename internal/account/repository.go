package account

import "context"

// Repository abstracts the storage for Accounts.
// It can be implemented with SQLite, Postgres, MySQL, etc.
type Repository interface {
	Create(ctx context.Context, acc *Account) error
	FindByUsername(ctx context.Context, username string) (*Account, error)
	FindByID(ctx context.Context, id uint) (*Account, error)
	ListAll(ctx context.Context) ([]Account, error)
}
