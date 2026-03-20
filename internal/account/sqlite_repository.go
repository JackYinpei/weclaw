package account

import (
	"context"

	"gorm.io/gorm"
)

type sqliteRepository struct {
	db *gorm.DB
}

// NewSQLiteRepository creates an abstract Repository for SQLite backend.
func NewSQLiteRepository(db *gorm.DB) Repository {
	// Abstracting SQLite-specific logic, but GORM mostly hides it
	// Automatic migration for the accounts table
	err := db.AutoMigrate(&Account{})
	if err != nil {
		panic("Failed to migrate account tables: " + err.Error())
	}

	return &sqliteRepository{db: db}
}

func (r *sqliteRepository) Create(ctx context.Context, acc *Account) error {
	return r.db.WithContext(ctx).Create(acc).Error
}

func (r *sqliteRepository) FindByUsername(ctx context.Context, username string) (*Account, error) {
	var acc Account
	err := r.db.WithContext(ctx).Where("username = ?", username).First(&acc).Error
	if err != nil {
		return nil, err
	}
	return &acc, nil
}

func (r *sqliteRepository) FindByID(ctx context.Context, id uint) (*Account, error) {
	var acc Account
	err := r.db.WithContext(ctx).First(&acc, id).Error
	if err != nil {
		return nil, err
	}
	return &acc, nil
}

func (r *sqliteRepository) ListAll(ctx context.Context) ([]Account, error) {
	var accounts []Account
	err := r.db.WithContext(ctx).Order("username ASC").Find(&accounts).Error
	return accounts, err
}
