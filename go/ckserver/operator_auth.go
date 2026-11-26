package ckserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/ckserver/store"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
)

// OperatorClaims are embedded inside the short-lived EdDSA Operator JWT.
type OperatorClaims struct {
	OperatorID   string `json:"oid"`
	Username     string `json:"usr"`
	DeviceID     string `json:"did"`
	TokenVersion int    `json:"ver"`
	jwt.RegisteredClaims
}

// OperatorLoginResponse is returned to the tablet upon successful operator authentication.
type OperatorLoginResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresAtMs int64  `json:"expires_at_ms"`
	Operator    struct {
		ID        string `json:"id"`
		Username  string `json:"username"`
		IsDefault bool   `json:"is_default"`
	} `json:"operator"`
}

// BoothCredentialHint is displayed by cklauncher during booth setup.
type BoothCredentialHint struct {
	Username        string `json:"username"`
	IsDefault       bool   `json:"is_default"`
	DefaultPassword string `json:"default_password,omitempty"` // Only populated while default credentials are active
}

// OperatorAuth manages operator accounts (Argon2id) and EdDSA JWT issuance/verification.
type OperatorAuth struct {
	mu          sync.RWMutex
	identity    *ServerIdentity
	store       *store.Store
	tokenTTL    time.Duration
	defaultUser string
	defaultPass string
}

func NewOperatorAuth(identity *ServerIdentity, st *store.Store, defaultUser, defaultPass string, tokenTTL time.Duration) (*OperatorAuth, error) {
	if defaultUser == "" {
		defaultUser = "operator"
	}
	if defaultPass == "" {
		defaultPass = "operator"
	}
	if tokenTTL <= 0 {
		tokenTTL = 30 * time.Minute
	}
	oa := &OperatorAuth{
		identity:    identity,
		store:       st,
		tokenTTL:    tokenTTL,
		defaultUser: defaultUser,
		defaultPass: defaultPass,
	}

	// Seed default operator ("operator" / "operator") if none exist yet.
	ctx := context.Background()
	count, err := st.CountOperators(ctx)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		hash, err := hashPassword(defaultPass)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		if err := st.CreateOperator(ctx, store.OperatorRecord{
			ID:           "op_" + cktelemetry.NewEventID(),
			Username:     defaultUser,
			PasswordHash: hash,
			IsDefault:    true,
			TokenVersion: 1,
			CreatedAtMs:  now,
			UpdatedAtMs:  now,
		}); err != nil {
			return nil, err
		}
	}
	return oa, nil
}

// BoothHint returns the primary operator display info for cklauncher's booth setup UI.
func (o *OperatorAuth) BoothHint(ctx context.Context) BoothCredentialHint {
	ops, err := o.store.ListOperators(ctx)
	if err != nil || len(ops) == 0 {
		return BoothCredentialHint{Username: o.defaultUser, IsDefault: true, DefaultPassword: o.defaultPass}
	}
	first := ops[0]
	hint := BoothCredentialHint{
		Username:  first.Username,
		IsDefault: first.IsDefault,
	}
	if first.IsDefault {
		hint.DefaultPassword = o.defaultPass
	}
	return hint
}

func (o *OperatorAuth) Login(ctx context.Context, username, password, deviceID string) (OperatorLoginResponse, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return OperatorLoginResponse{}, ckframework.ErrValidation.New("username and password are required")
	}
	op, err := o.store.GetOperatorByUsername(ctx, username)
	if err != nil {
		return OperatorLoginResponse{}, err
	}
	if op == nil || !verifyPassword(op.PasswordHash, password) {
		return OperatorLoginResponse{}, ckframework.ErrUnauthorized.New("invalid operator username or password").WithProperty(ckframework.PropCode, "invalid_credentials")
	}
	return o.issueLoginResponse(op, deviceID)
}

func (o *OperatorAuth) Refresh(ctx context.Context, operatorID, deviceID string) (OperatorLoginResponse, error) {
	op, err := o.store.GetOperatorByID(ctx, operatorID)
	if err != nil {
		return OperatorLoginResponse{}, err
	}
	if op == nil {
		return OperatorLoginResponse{}, ckframework.ErrUnauthorized.New("operator account no longer exists")
	}
	return o.issueLoginResponse(op, deviceID)
}

func (o *OperatorAuth) issueLoginResponse(op *store.OperatorRecord, deviceID string) (OperatorLoginResponse, error) {
	now := time.Now()
	exp := now.Add(o.tokenTTL)
	claims := OperatorClaims{
		OperatorID:   op.ID,
		Username:     op.Username,
		DeviceID:     deviceID,
		TokenVersion: op.TokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "corekiosk-operator:" + o.identity.ServerID,
			Subject:   op.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signed, err := tok.SignedString(o.identity.PrivateKey)
	if err != nil {
		return OperatorLoginResponse{}, ckframework.ErrInternal.Wrap(err, "sign operator jwt")
	}
	var resp OperatorLoginResponse
	resp.AccessToken = signed
	resp.ExpiresAtMs = exp.UnixMilli()
	resp.Operator.ID = op.ID
	resp.Operator.Username = op.Username
	resp.Operator.IsDefault = op.IsDefault
	return resp, nil
}

// VerifyToken checks signature, expiry, device binding, and token_version against SQLite.
func (o *OperatorAuth) VerifyToken(ctx context.Context, rawToken, expectedDeviceID string) (*OperatorClaims, error) {
	var claims OperatorClaims
	tok, err := jwt.ParseWithClaims(rawToken, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodEdDSA {
			return nil, ckframework.ErrUnauthorized.New("unexpected jwt signing method")
		}
		return o.identity.PublicKey, nil
	})
	if err != nil || !tok.Valid {
		return nil, ckframework.ErrOperatorTokenExpired.New("operator session expired; please log in again")
	}
	if expectedDeviceID != "" && claims.DeviceID != expectedDeviceID {
		return nil, ckframework.ErrForbidden.New("operator token is bound to another device")
	}
	op, err := o.store.GetOperatorByID(ctx, claims.OperatorID)
	if err != nil {
		return nil, err
	}
	if op == nil || op.TokenVersion != claims.TokenVersion {
		return nil, ckframework.ErrOperatorTokenExpired.New("operator credentials changed; please log in again")
	}
	return &claims, nil
}

func (o *OperatorAuth) CreateOperator(ctx context.Context, username, password string) (store.OperatorRecord, error) {
	username = strings.TrimSpace(username)
	if len(username) < 2 || len(username) > 48 {
		return store.OperatorRecord{}, ckframework.ErrValidation.New("username must be 2-48 characters")
	}
	if len(password) < 4 || len(password) > 128 {
		return store.OperatorRecord{}, ckframework.ErrValidation.New("password must be 4-128 characters")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return store.OperatorRecord{}, err
	}
	now := time.Now().UnixMilli()
	rec := store.OperatorRecord{
		ID:           "op_" + cktelemetry.NewEventID(),
		Username:     username,
		PasswordHash: hash,
		IsDefault:    false,
		TokenVersion: 1,
		CreatedAtMs:  now,
		UpdatedAtMs:  now,
	}
	if err := o.store.CreateOperator(ctx, rec); err != nil {
		return store.OperatorRecord{}, err
	}
	return rec, nil
}

func (o *OperatorAuth) UpdateCredentials(ctx context.Context, id, newUsername, newPassword string) error {
	newUsername = strings.TrimSpace(newUsername)
	if newUsername != "" && (len(newUsername) < 2 || len(newUsername) > 48) {
		return ckframework.ErrValidation.New("username must be 2-48 characters")
	}
	var hash string
	if newPassword != "" {
		if len(newPassword) < 4 || len(newPassword) > 128 {
			return ckframework.ErrValidation.New("password must be 4-128 characters")
		}
		var err error
		hash, err = hashPassword(newPassword)
		if err != nil {
			return err
		}
	}
	isDef := newUsername == o.defaultUser && newPassword == o.defaultPass
	return o.store.UpdateOperatorCredentials(ctx, id, newUsername, hash, isDef, time.Now().UnixMilli())
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", ckframework.ErrInternal.Wrap(err, "generate password salt")
	}
	key := argon2.IDKey([]byte(password), salt, 1, 32*1024, 2, 32)
	return fmt.Sprintf("argon2id$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 1, 32*1024, 2, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
