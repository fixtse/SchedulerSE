package domain

import "errors"

type NotificationType string

const (
	NotificationSuccess  NotificationType = "success"
	NotificationFailure  NotificationType = "failed"
	NotificationFallback NotificationType = "fallback"
)

type TelegramSettings struct {
	Token          string `json:"-"`
	Chat           string `json:"-"`
	Enabled        bool   `json:"enabled"`
	Revision       int64  `json:"revision"`
	NotifySuccess  bool   `json:"notify_success"`
	NotifyFailure  bool   `json:"notify_failure"`
	NotifyFallback bool   `json:"notify_fallback"`
}

func (s TelegramSettings) Accepts(kind NotificationType) bool {
	if !s.Enabled {
		return false
	}
	switch kind {
	case NotificationSuccess:
		return s.NotifySuccess
	case NotificationFailure:
		return s.NotifyFailure
	case NotificationFallback:
		return s.NotifyFallback
	default:
		return false
	}
}

var ErrNotificationsDisabled = errors.New("notifications disabled")
