package ckframework

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"
)

const MaxAssetBytes = 12 * 1024 * 1024 // 12 MiB cap for booth image uploads

// SeedAssetSpec registers an embedded default media asset (e.g. via //go:embed) on AppSpec.
// On server boot, seed assets are idempotently written into the SQLite assets store using
// their deterministic content-addressed ID ("ast_" + sha256[:24]).
type SeedAssetSpec struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Data     []byte `json:"-"`
}

// AssetMeta describes a stored image/media asset without carrying its raw bytes.
type AssetMeta struct {
	ID          string `json:"id"`
	SHA256      string `json:"sha256"`
	Filename    string `json:"filename,omitempty"`
	MimeType    string `json:"mime_type"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	SeedKey     string `json:"seed_key,omitempty"`
	CreatedAtMs int64  `json:"created_at_ms"`
}

// AssetIDFromBytes returns the deterministic content-addressed asset ID ("ast_<24hex>") and full SHA-256 hex.
func AssetIDFromBytes(data []byte) (id string, shaHex string) {
	sum := sha256.Sum256(data)
	shaHex = hex.EncodeToString(sum[:])
	return "ast_" + shaHex[:24], shaHex
}

// SeedAsset constructs a SeedAssetSpec with a deterministic content-addressed ID so default
// CatalogEntries and Settings can reference seedAsset.ID directly in AppSpec declarations.
func SeedAsset(key, filename string, data []byte) SeedAssetSpec {
	id, _ := AssetIDFromBytes(data)
	mime, _, _, err := InspectImageBytes(data)
	if err != nil {
		mime = "application/octet-stream"
	}
	return SeedAssetSpec{
		ID:       id,
		Key:      key,
		Filename: filename,
		MimeType: mime,
		Data:     data,
	}
}

// InspectImageBytes validates supported raster image formats (PNG, JPEG, WebP, GIF) and extracts dimensions.
func InspectImageBytes(data []byte) (mimeType string, width int, height int, err error) {
	if len(data) == 0 {
		return "", 0, 0, ErrValidation.New("empty image file").WithProperty(PropCode, "invalid_asset")
	}
	if len(data) > MaxAssetBytes {
		return "", 0, 0, ErrValidation.New("image exceeds %d MB limit", MaxAssetBytes/(1024*1024)).WithProperty(PropCode, "asset_too_large")
	}

	// Detect RIFF/WEBP explicitly before image.DecodeConfig so stdlib needs no extra codec import.
	if len(data) >= 30 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		w, h := parseWebPDimensions(data)
		return "image/webp", w, h, nil
	}

	cfg, format, decErr := image.DecodeConfig(bytes.NewReader(data))
	if decErr != nil {
		sniff := http.DetectContentType(data)
		return "", 0, 0, ErrValidation.New("unsupported image format (%s); upload PNG, JPEG, WebP, or GIF", sniff).WithProperty(PropCode, "unsupported_asset")
	}
	switch strings.ToLower(format) {
	case "png":
		mimeType = "image/png"
	case "jpeg", "jpg":
		mimeType = "image/jpeg"
	case "gif":
		mimeType = "image/gif"
	default:
		mimeType = "image/" + strings.ToLower(format)
	}
	return mimeType, cfg.Width, cfg.Height, nil
}

func parseWebPDimensions(data []byte) (int, int) {
	chunk := string(data[12:16])
	switch chunk {
	case "VP8X":
		if len(data) >= 30 {
			w := 1 + int(uint32(data[24])|uint32(data[25])<<8|uint32(data[26])<<16)
			h := 1 + int(uint32(data[27])|uint32(data[28])<<8|uint32(data[29])<<16)
			return w, h
		}
	case "VP8L":
		if len(data) >= 25 && data[20] == 0x2f {
			bits := binary.LittleEndian.Uint32(data[21:25])
			w := int(bits&0x3FFF) + 1
			h := int((bits>>14)&0x3FFF) + 1
			return w, h
		}
	case "VP8 ":
		if len(data) >= 30 && data[23] == 0x9d && data[24] == 0x01 && data[25] == 0x2a {
			w := int(binary.LittleEndian.Uint16(data[26:28]) & 0x3FFF)
			h := int(binary.LittleEndian.Uint16(data[28:30]) & 0x3FFF)
			return w, h
		}
	}
	return 0, 0
}
