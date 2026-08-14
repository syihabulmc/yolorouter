package service

import (
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/repository"
)

// UserSummaryView is the list shape for the admin's user directory and the
// data source behind "filter by user" dropdowns. Deliberately excludes the
// lockout internals and any credential material — the model's json:"-" tags
// already guard those, but this view narrows the wire contract to exactly
// what the admin UI consumes.
type UserSummaryView struct {
	ID          uint       `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Status      int        `json:"status"`
	IsLocal     bool       `json:"is_local"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// ListUsers returns every account for the admin UI.
func ListUsers(db *gorm.DB) ([]UserSummaryView, error) {
	users, err := repository.ListUsers(db)
	if err != nil {
		return nil, err
	}
	views := make([]UserSummaryView, 0, len(users))
	for _, u := range users {
		views = append(views, UserSummaryView{
			ID:          u.ID,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Email:       u.Email,
			Role:        u.Role,
			Status:      u.Status,
			IsLocal:     u.IsLocal,
			LastLoginAt: u.LastLoginAt,
			CreatedAt:   u.CreatedAt,
		})
	}
	return views, nil
}
