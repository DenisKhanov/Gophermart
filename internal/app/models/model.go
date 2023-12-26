package models

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"time"
)

type CTXKey string

const UserIDKey CTXKey = "userID"

type UserRegistered struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type UserWithdrawal struct {
	Order       string           `json:"order"`
	Sum         *decimal.Decimal `json:"sum,omitempty"`
	ProcessedAt *time.Time       `json:"processed_at,omitempty"`
}

type UserOrder struct {
	UserID     uuid.UUID        `json:"-"`
	Number     string           `json:"number"`
	Status     string           `json:"status"`
	Accrual    *decimal.Decimal `json:"accrual,omitempty"`
	UploadedAt time.Time        `json:"uploaded_at"`
}
type AccrualResponseData struct {
	UserID  uuid.UUID
	Order   string           `json:"order"`
	Status  string           `json:"status"`
	Accrual *decimal.Decimal `json:"accrual,omitempty"`
}
type BalanceResponseData struct {
	Current   decimal.Decimal `json:"current"`
	Withdrawn decimal.Decimal `json:"withdrawn"`
}
