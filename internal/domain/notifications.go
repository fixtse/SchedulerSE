package domain

import "errors"

type TelegramSettings struct {
	Token    string `json:"-"`
	Chat     string `json:"-"`
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
}

var ErrNotificationsDisabled = errors.New("notifications disabled")
