package ckserver

import (
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/gin-gonic/gin"
	"gopkg.in/guregu/null.v4"
)

func (s *Server) handleGetAsset(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	meta, data, err := s.store.GetAsset(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	etag := `"` + meta.SHA256 + `"`
	c.Header("ETag", etag)
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, meta.MimeType, data)
}

func (s *Server) handleListAssets(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	items, err := s.store.ListAssets(c.Request.Context(), limit)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"assets": items})
}

func (s *Server) handleUploadAsset(c *gin.Context) {
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)

	var filename string
	var raw []byte

	ct := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.HasPrefix(ct, "multipart/form-data") {
		fh, err := c.FormFile("file")
		if err != nil {
			respondError(c, ckframework.ErrValidation.Wrap(err, "missing multipart 'file' field"))
			return
		}
		if fh.Size > ckframework.MaxAssetBytes {
			respondError(c, ckframework.ErrValidation.New("image exceeds %d MB limit", ckframework.MaxAssetBytes/(1024*1024)))
			return
		}
		f, err := fh.Open()
		if err != nil {
			respondError(c, ckframework.ErrValidation.Wrap(err, "open uploaded file"))
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, ckframework.MaxAssetBytes+1))
		if err != nil {
			respondError(c, ckframework.ErrValidation.Wrap(err, "read uploaded file"))
			return
		}
		filename = fh.Filename
		raw = data
	} else {
		var body struct {
			Filename   string `json:"filename"`
			DataBase64 string `json:"data_base64"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			respondError(c, ckframework.ErrValidation.Wrap(err, "invalid asset upload payload"))
			return
		}
		b64 := strings.TrimSpace(body.DataBase64)
		if comma := strings.Index(b64, ","); comma >= 0 && strings.HasPrefix(b64, "data:") {
			b64 = b64[comma+1:]
		}
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(b64)
			if err != nil {
				respondError(c, ckframework.ErrValidation.Wrap(err, "invalid base64 image data"))
				return
			}
		}
		filename = body.Filename
		raw = decoded
	}

	now := time.Now().UnixMilli()
	meta, err := s.store.PutAsset(c.Request.Context(), filename, "", raw, now)
	if err != nil {
		respondError(c, err)
		return
	}
	if s.outbox != nil {
		_, _ = s.outbox.Record(c.Request.Context(), cktelemetry.Event{
			Category: "audit",
			Kind:     "asset.uploaded",
			Severity: "info",
			Payload: map[string]any{
				"asset_id":   meta.ID,
				"filename":   meta.Filename,
				"mime_type":  meta.MimeType,
				"width":      meta.Width,
				"height":     meta.Height,
				"size_bytes": meta.SizeBytes,
				"actor":      op.Username,
			},
		})
	}
	c.JSON(http.StatusCreated, gin.H{
		"asset": meta,
		"url":   "/v1/assets/" + meta.ID,
	})
}

func (s *Server) handleListCatalogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"catalogs": s.hub.Catalogs()})
}

func (s *Server) handleGetCatalog(c *gin.Context) {
	id := c.Param("id")
	view, ok := s.hub.Catalog(id)
	if !ok {
		respondError(c, ckframework.ErrNotFound.New("catalog %q not found", id))
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleReplaceCatalog(c *gin.Context) {
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)
	var body struct {
		ExpectedRevision null.Int                   `json:"expected_revision"`
		Entries          []ckframework.CatalogEntry `json:"entries"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Entries == nil {
		respondError(c, ckframework.ErrValidation.New("entries array is required"))
		return
	}
	expected := -1
	if body.ExpectedRevision.Valid {
		expected = int(body.ExpectedRevision.Int64)
	}
	view, err := s.hub.SaveCatalogEntries(c.Request.Context(), c.Param("id"), expected, body.Entries, op.Username)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleAddCatalogEntry(c *gin.Context) {
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)
	var entry ckframework.CatalogEntry
	if err := c.ShouldBindJSON(&entry); err != nil {
		respondError(c, ckframework.ErrValidation.Wrap(err, "invalid catalog entry payload"))
		return
	}
	// Default new items to enabled=true when omitted by caller unless explicitly false in JSON.
	if entry.Fields == nil {
		entry.Fields = map[string]any{}
	}
	view, created, err := s.hub.AddCatalogEntry(c.Request.Context(), c.Param("id"), entry, op.Username)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"catalog": view, "entry": created})
}

func (s *Server) handlePatchCatalogEntry(c *gin.Context) {
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)
	var patch CatalogEntryPatch
	if err := c.ShouldBindJSON(&patch); err != nil {
		respondError(c, ckframework.ErrValidation.Wrap(err, "invalid catalog patch payload"))
		return
	}
	view, updated, err := s.hub.PatchCatalogEntry(c.Request.Context(), c.Param("id"), c.Param("entry_id"), patch, op.Username)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"catalog": view, "entry": updated})
}

func (s *Server) handleDeleteCatalogEntry(c *gin.Context) {
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)
	view, err := s.hub.DeleteCatalogEntry(c.Request.Context(), c.Param("id"), c.Param("entry_id"), op.Username)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleListRulesets(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"rulesets": s.hub.Rulesets()})
}

func (s *Server) handleGetRuleset(c *gin.Context) {
	id := c.Param("id")
	view, ok := s.hub.Ruleset(id)
	if !ok {
		respondError(c, ckframework.ErrNotFound.New("ruleset %q not found", id))
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleSaveRuleset(c *gin.Context) {
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)
	var body struct {
		ExpectedRevision null.Int                `json:"expected_revision"`
		Rules            []ckframework.RuleEntry `json:"rules"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Rules == nil {
		respondError(c, ckframework.ErrValidation.New("rules array is required"))
		return
	}
	expected := -1
	if body.ExpectedRevision.Valid {
		expected = int(body.ExpectedRevision.Int64)
	}
	view, err := s.hub.SaveRuleset(c.Request.Context(), c.Param("id"), expected, body.Rules, op.Username)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}
