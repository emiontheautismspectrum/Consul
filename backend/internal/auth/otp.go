package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"finadvisor/internal/infrastructure/ratelimit"
)

const (
	otpTTL         = 5 * time.Minute
	otpLockTTL     = 10 * time.Minute
	otpMaxWrong    = 3
	otpSendLimit   = 3  // codes per otpSendWindow per phone
	otpSendLimitIP = 30 // per IP: a venue Wi-Fi shares one NAT address
	otpSendWindow  = 10 * time.Minute
)

var (
	ErrInvalidCode    = errors.New("invalid or expired code")
	ErrOTPLocked      = errors.New("too many wrong codes, try again later")
	ErrOTPRateLimited = errors.New("too many codes requested, try again later")
)

// SMSSender delivers the login code. TODO: a real provider (Eskiz or Play Mobile).
type SMSSender interface {
	Send(ctx context.Context, phone, text string) error
}

// LogSender is for development: it records that a code was sent, never the code itself.
type LogSender struct{}

func (LogSender) Send(ctx context.Context, phone, _ string) error {
	slog.InfoContext(ctx, "otp: code sent (log sender, no SMS)", "phone", MaskPhone(phone))
	return nil
}

// FixedSender is used with APP_ENV=demo: the code is DEMO_OTP and nothing is sent.
type FixedSender struct{}

func (FixedSender) Send(context.Context, string, string) error { return nil }

// OTPStore is the part of Redis the OTP flow needs.
type OTPStore interface {
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
	GetJSON(ctx context.Context, key string, dst any) (bool, error)
	Delete(ctx context.Context, key string) error
	Take(ctx context.Context, key string, limit int, window time.Duration) (ratelimit.Result, error)
}

type OTP struct {
	store     OTPStore
	sender    SMSSender
	pepper    []byte
	fixedCode string // non-empty only in demo mode
}

// NewOTP builds the OTP flow. fixedCode != "" switches to demo mode (every code equals it).
func NewOTP(store OTPStore, sender SMSSender, pepper, fixedCode string) *OTP {
	return &OTP{store: store, sender: sender, pepper: []byte(pepper), fixedCode: fixedCode}
}

type otpRecord struct {
	Hash string `json:"h"`
}

// Keys hash the phone: Redis never stores numbers in clear text.
func (o *OTP) key(kind, phone string) string {
	sum := sha256.Sum256(append([]byte(phone), o.pepper...))
	return "otp:" + kind + ":" + hex.EncodeToString(sum[:12])
}

func (o *OTP) hashCode(phone, code string) string {
	sum := sha256.Sum256([]byte(phone + "|" + code + "|" + string(o.pepper)))
	return hex.EncodeToString(sum[:])
}

func (o *OTP) locked(ctx context.Context, phone string) (bool, error) {
	var v int
	return o.store.GetJSON(ctx, o.key("lock", phone), &v)
}

// Send generates and delivers a code. phone must already be normalized.
func (o *OTP) Send(ctx context.Context, phone, ip string) error {
	if l, err := o.locked(ctx, phone); err != nil {
		return err
	} else if l {
		return ErrOTPLocked
	}
	for _, l := range []struct {
		key   string
		limit int
	}{{"send:" + o.key("p", phone), otpSendLimit}, {"send:ip:" + ip, otpSendLimitIP}} {
		res, err := o.store.Take(ctx, l.key, l.limit, otpSendWindow)
		if err != nil {
			return err
		}
		if !res.Allowed {
			return ErrOTPRateLimited
		}
	}
	code := o.fixedCode
	if code == "" {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return err
		}
		code = fmt.Sprintf("%06d", n.Int64())
	}
	if err := o.store.SetJSON(ctx, o.key("code", phone), otpRecord{Hash: o.hashCode(phone, code)}, otpTTL); err != nil {
		return err
	}
	return o.sender.Send(ctx, phone, "Consul: kirish kodi "+code)
}

// Verify checks the code. otpMaxWrong wrong codes lock the phone for otpLockTTL.
func (o *OTP) Verify(ctx context.Context, phone, code string) error {
	if l, err := o.locked(ctx, phone); err != nil {
		return err
	} else if l {
		return ErrOTPLocked
	}
	var rec otpRecord
	found, err := o.store.GetJSON(ctx, o.key("code", phone), &rec)
	if err != nil {
		return err
	}
	if found && subtle.ConstantTimeCompare([]byte(rec.Hash), []byte(o.hashCode(phone, code))) == 1 {
		_ = o.store.Delete(ctx, o.key("code", phone)) // single use
		_ = o.store.Delete(ctx, "rl:"+o.key("wrong", phone))
		return nil
	}
	res, err := o.store.Take(ctx, o.key("wrong", phone), otpMaxWrong-1, otpLockTTL)
	if err != nil {
		return err
	}
	if !res.Allowed {
		_ = o.store.Delete(ctx, o.key("code", phone))
		if err := o.store.SetJSON(ctx, o.key("lock", phone), 1, otpLockTTL); err != nil {
			return err
		}
		return ErrOTPLocked
	}
	return ErrInvalidCode
}
