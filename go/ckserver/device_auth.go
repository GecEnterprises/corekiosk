package ckserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/ckserver/store"
	"github.com/golang-jwt/jwt/v5"
)

type challengeEntry struct {
	nonce     string
	expiresAt time.Time
}

// DeviceAuth manages one-time pairing and zero-PIN Ed25519 challenge-response authentication.
type DeviceAuth struct {
	mu          sync.Mutex
	identity    *ServerIdentity
	store       *store.Store
	pairCode    string
	pairExpires time.Time
	challenges  map[string]challengeEntry
	tokenTTL    time.Duration
	onRotate    func()
}

type deviceJWTClaims struct {
	DeviceID string `json:"did"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

func NewDeviceAuth(identity *ServerIdentity, st *store.Store) *DeviceAuth {
	da := &DeviceAuth{
		identity:   identity,
		store:      st,
		challenges: make(map[string]challengeEntry),
		tokenTTL:   24 * time.Hour,
	}
	da.RotatePairCode()
	return da
}

func (d *DeviceAuth) SetOnRotate(fn func()) {
	d.mu.Lock()
	d.onRotate = fn
	d.mu.Unlock()
}

func (d *DeviceAuth) RotatePairCode() string {
	d.mu.Lock()
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	d.pairCode = fmt.Sprintf("%06d", n.Int64())
	d.pairExpires = time.Now().Add(15 * time.Minute)
	code := d.pairCode
	cb := d.onRotate
	d.mu.Unlock()
	if cb != nil {
		cb()
	}
	return code
}

func (d *DeviceAuth) PairCode() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Now().After(d.pairExpires) {
		n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
		d.pairCode = fmt.Sprintf("%06d", n.Int64())
		d.pairExpires = time.Now().Add(15 * time.Minute)
	}
	return d.pairCode
}

// IssueChallenge generates a single-use cryptographic nonce signed by the server's Ed25519 key
// for mutual authentication (client verifies server_sig against pinned server_pub).
func (d *DeviceAuth) IssueChallenge() (nonce string, serverSigB64 string) {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	nonce = hex.EncodeToString(raw)
	sig := ed25519.Sign(d.identity.PrivateKey, []byte(nonce))
	serverSigB64 = base64.StdEncoding.EncodeToString(sig)

	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for k, v := range d.challenges {
		if now.After(v.expiresAt) {
			delete(d.challenges, k)
		}
	}
	d.challenges[nonce] = challengeEntry{nonce: nonce, expiresAt: now.Add(2 * time.Minute)}
	return nonce, serverSigB64
}

func (d *DeviceAuth) consumeChallenge(nonce string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.challenges[nonce]
	if !ok {
		return false
	}
	delete(d.challenges, nonce)
	return time.Now().Before(c.expiresAt)
}

// PairRequest is submitted once per tablet during initial key exchange.
type PairRequest struct {
	ClientPub  string                 `json:"client_pub"`
	PairCode   string                 `json:"pair_code"`
	Nonce      string                 `json:"nonce,omitempty"`
	Proof      string                 `json:"proof"` // base64(sign(client_priv, server_pub + nonce))
	Role       string                 `json:"role,omitempty"`
	DeviceInfo ckframework.DeviceInfo `json:"device_info"`
}

// PairResponse returns server identity + server signature proving mutual key possession.
type PairResponse struct {
	DeviceID    string `json:"device_id"`
	ServerID    string `json:"server_id"`
	ServerPub   string `json:"server_pub"`
	ServerSig   string `json:"server_sig"`
	DeviceToken string `json:"device_token"`
}

func (d *DeviceAuth) Pair(ctx context.Context, req PairRequest, remoteIP string) (PairResponse, ckframework.DeviceRecord, error) {
	expectedCode := d.PairCode()
	code := strings.TrimSpace(req.PairCode)
	if len(code) != len(expectedCode) || subtle.ConstantTimeCompare([]byte(code), []byte(expectedCode)) != 1 {
		return PairResponse{}, ckframework.DeviceRecord{}, ckframework.ErrForbidden.New("invalid pairing code").WithProperty(ckframework.PropCode, "invalid_pair_code")
	}

	pubBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.ClientPub))
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return PairResponse{}, ckframework.DeviceRecord{}, ckframework.ErrValidation.New("client_pub must be a base64 Ed25519 public key")
	}
	pub := ed25519.PublicKey(pubBytes)

	proofBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.Proof))
	if err != nil || len(proofBytes) != ed25519.SignatureSize {
		return PairResponse{}, ckframework.DeviceRecord{}, ckframework.ErrValidation.New("proof must be a valid Ed25519 signature")
	}
	msg := []byte(d.identity.PubBase64 + req.Nonce)
	if !ed25519.Verify(pub, msg, proofBytes) {
		return PairResponse{}, ckframework.DeviceRecord{}, ckframework.ErrUnauthorized.New("client key proof verification failed")
	}

	deviceID := DeviceIDFromPub(pub)
	role := req.Role
	if role != "operator_tablet" {
		role = "tablet"
	}
	name := strings.TrimSpace(req.DeviceInfo.DeviceName)
	if name == "" {
		name = "Tablet " + deviceID[:6]
	}
	devType := strings.TrimSpace(req.DeviceInfo.DeviceType)
	if devType == "" {
		devType = "android_tablet"
	}
	protoVer := req.DeviceInfo.ProtocolVersion
	if protoVer <= 0 {
		protoVer = ckframework.ProtocolVersion
	}

	now := time.Now().UnixMilli()
	rec := ckframework.DeviceRecord{
		DeviceID:         deviceID,
		PublicKey:        base64.StdEncoding.EncodeToString(pub),
		Role:             role,
		DeviceName:       name,
		DeviceType:       devType,
		Manufacturer:     req.DeviceInfo.Manufacturer,
		Model:            req.DeviceInfo.Model,
		OSName:           req.DeviceInfo.OSName,
		OSVersion:        req.DeviceInfo.OSVersion,
		AppVersion:       req.DeviceInfo.AppVersion,
		FrameworkVersion: req.DeviceInfo.FrameworkVersion,
		ProtocolVersion:  protoVer,
		DeviceInfo:       req.DeviceInfo.Extra,
		AllowCloudRelay:  true,
		LastIP:           remoteIP,
		PairedAtMs:       now,
		LastSeenAtMs:     now,
	}
	if err := d.store.UpsertDevice(ctx, rec); err != nil {
		return PairResponse{}, ckframework.DeviceRecord{}, err
	}

	serverSig := ed25519.Sign(d.identity.PrivateKey, []byte(rec.PublicKey+d.identity.ServerID))
	tok, err := d.IssueDeviceToken(rec.DeviceID, rec.Role)
	if err != nil {
		return PairResponse{}, ckframework.DeviceRecord{}, err
	}
	return PairResponse{
		DeviceID:    rec.DeviceID,
		ServerID:    d.identity.ServerID,
		ServerPub:   d.identity.PubBase64,
		ServerSig:   base64.StdEncoding.EncodeToString(serverSig),
		DeviceToken: tok,
	}, rec, nil
}

// VerifyChallenge authenticates an already-paired client using its persistent Ed25519 key (zero PIN).
func (d *DeviceAuth) VerifyChallenge(ctx context.Context, clientID, nonce, sigB64 string, info *ckframework.DeviceInfo, remoteIP string) (*ckframework.DeviceRecord, string, error) {
	if !d.consumeChallenge(nonce) {
		return nil, "", ckframework.ErrUnauthorized.New("challenge expired or unknown").WithProperty(ckframework.PropCode, "invalid_challenge")
	}
	rec, err := d.store.GetDevice(ctx, clientID)
	if err != nil {
		return nil, "", err
	}
	if rec == nil || rec.RevokedAtMs != nil {
		return nil, "", ckframework.ErrUnauthorized.New("device is not paired or has been revoked").WithProperty(ckframework.PropCode, "device_unpaired")
	}
	pubBytes, err := base64.StdEncoding.DecodeString(rec.PublicKey)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return nil, "", ckframework.ErrStore.New("stored device public key is invalid")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return nil, "", ckframework.ErrUnauthorized.New("invalid signature format")
	}
	msg := []byte(nonce + d.identity.PubBase64)
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), msg, sigBytes) {
		return nil, "", ckframework.ErrUnauthorized.New("challenge signature mismatch")
	}

	now := time.Now().UnixMilli()
	if info != nil {
		if info.DeviceName != "" {
			rec.DeviceName = info.DeviceName
		}
		if info.DeviceType != "" {
			rec.DeviceType = info.DeviceType
		}
		if info.Manufacturer != "" {
			rec.Manufacturer = info.Manufacturer
		}
		if info.Model != "" {
			rec.Model = info.Model
		}
		if info.OSName != "" {
			rec.OSName = info.OSName
		}
		if info.OSVersion != "" {
			rec.OSVersion = info.OSVersion
		}
		if info.AppVersion != "" {
			rec.AppVersion = info.AppVersion
		}
		if info.FrameworkVersion != "" {
			rec.FrameworkVersion = info.FrameworkVersion
		}
		if info.ProtocolVersion > 0 {
			rec.ProtocolVersion = info.ProtocolVersion
		}
		if info.Extra != nil {
			rec.DeviceInfo = info.Extra
		}
		rec.LastIP = remoteIP
		rec.LastSeenAtMs = now
		_ = d.store.UpsertDevice(ctx, *rec)
	} else {
		_ = d.store.TouchDevice(ctx, rec.DeviceID, remoteIP, now)
	}

	tok, err := d.IssueDeviceToken(rec.DeviceID, rec.Role)
	if err != nil {
		return nil, "", err
	}
	return rec, tok, nil
}

func (d *DeviceAuth) IssueDeviceToken(deviceID, role string) (string, error) {
	now := time.Now()
	claims := deviceJWTClaims{
		DeviceID: deviceID,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "corekiosk-device:" + d.identity.ServerID,
			Subject:   deviceID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(d.tokenTTL)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signed, err := tok.SignedString(d.identity.PrivateKey)
	if err != nil {
		return "", ckframework.ErrInternal.Wrap(err, "sign device token")
	}
	return signed, nil
}

func (d *DeviceAuth) VerifyDeviceToken(ctx context.Context, rawToken string) (*ckframework.DeviceRecord, error) {
	if rawToken == "" {
		return nil, ckframework.ErrUnauthorized.New("missing device token")
	}
	var claims deviceJWTClaims
	tok, err := jwt.ParseWithClaims(rawToken, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodEdDSA {
			return nil, ckframework.ErrUnauthorized.New("unexpected signing method")
		}
		return d.identity.PublicKey, nil
	})
	if err != nil || !tok.Valid {
		return nil, ckframework.ErrUnauthorized.New("invalid or expired device token")
	}
	rec, err := d.store.GetDevice(ctx, claims.DeviceID)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.RevokedAtMs != nil {
		return nil, ckframework.ErrUnauthorized.New("paired device revoked")
	}
	return rec, nil
}
