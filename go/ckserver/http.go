package ckserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

const (
	ctxDeviceKey   = "ck_device"
	ctxOperatorKey = "ck_operator"
)

func (s *Server) registerRoutes(r *gin.Engine) {
	r.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Device-Token, X-Bridge-Token")
		c.Header("Access-Control-Expose-Headers", "Content-Disposition")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	v1 := r.Group("/v1")
	{
		// Public discovery & key exchange
		v1.GET("/health", s.handleHealth)
		v1.POST("/pair", s.handlePair)
		v1.POST("/auth/challenge", s.handleAuthChallenge)
		v1.POST("/auth/verify", s.handleAuthVerify)
		v1.GET("/events", s.handleEventsWS)
		v1.GET("/bridge", s.handleBridge)

		// Paired Device routes (zero PIN after pairing)
		dev := v1.Group("")
		dev.Use(s.requireDevice)
		{
			dev.GET("/schema", s.handleSchema)
			dev.GET("/state", s.handleState)
			dev.POST("/submissions", s.handleSubmitForm(false))
			dev.POST("/rounds", s.handleSubmitForm(true))
			dev.DELETE("/rounds/:id", s.handleCancelRound)
			dev.GET("/boards/:board_id", s.handleGetBoard)

			// Bidirectional local-first telemetry relay
			dev.POST("/telemetry/ingest", s.handleTelemetryIngest)
			dev.GET("/telemetry/pull", s.handleTelemetryPull)
			dev.POST("/telemetry/ack", s.handleTelemetryAck)

			// Operator login (requires paired device + operator username/password -> JWT)
			dev.POST("/operators/login", s.handleOperatorLogin)

			// Operator AuthGuard routes (paired device + valid Operator EdDSA JWT)
			op := dev.Group("")
			op.Use(s.requireOperator)
			{
				op.POST("/operators/refresh", s.handleOperatorRefresh)
				op.GET("/config", s.handleGetConfig)
				op.PATCH("/config", s.handlePatchConfig)
				op.POST("/actions/:id", s.handleTriggerAction)

				// Boards: DELETE row only — NO PATCH/PUT route exists so contents stay immutable.
				op.DELETE("/boards/:board_id/rows/:entry_id", s.handleDeleteBoardRow)

				// Immutable form submissions history: READ + EXPORT ONLY — NO DELETE/PATCH routes exist.
				op.GET("/submissions", s.handleListSubmissions)
				op.GET("/submissions/export.csv", s.handleExportSubmissionsCSV)

				// Fleet & operator administration
				op.GET("/devices", s.handleListDevices)
				op.DELETE("/devices/:id", s.handleRevokeDevice)
				op.GET("/operators", s.handleListOperators)
				op.POST("/operators", s.handleCreateOperator)
				op.PATCH("/operators/:id", s.handleUpdateOperator)
				op.DELETE("/operators/:id", s.handleDeleteOperator)
			}
		}
	}
}

func respondError(c *gin.Context, err error) {
	status, code, details := ckframework.HTTPStatus(err)
	body := gin.H{
		"code":  code,
		"error": err.Error(),
	}
	if details != nil {
		body["details"] = details
	}
	c.AbortWithStatusJSON(status, body)
}

func (s *Server) requireDevice(c *gin.Context) {
	token := strings.TrimSpace(c.GetHeader("X-Device-Token"))
	if token == "" {
		if bearer, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Device "); ok {
			token = strings.TrimSpace(bearer)
		}
	}
	rec, err := s.deviceAuth.VerifyDeviceToken(c.Request.Context(), token)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Set(ctxDeviceKey, rec)
	c.Next()
}

func (s *Server) requireOperator(c *gin.Context) {
	dev := c.MustGet(ctxDeviceKey).(*ckframework.DeviceRecord)
	raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(raw) == "" {
		respondError(c, ckframework.ErrUnauthorized.New("operator authentication required").WithProperty(ckframework.PropCode, "operator_auth_required"))
		return
	}
	claims, err := s.operatorAuth.VerifyToken(c.Request.Context(), strings.TrimSpace(raw), dev.DeviceID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Set(ctxOperatorKey, claims)
	c.Next()
}

func (s *Server) handleHealth(c *gin.Context) {
	snap := s.hub.Snapshot()
	resp := ckframework.HealthResponse{
		Service:          "corekiosk",
		AppName:          s.spec.Name,
		Versions:         snap.Versions,
		MinClientVersion: s.spec.MinClientVersion,
		ServerID:         s.identity.ServerID,
		ServerPub:        s.identity.PubBase64,
		BootID:           s.bootID,
		Phase:            snap.Phase,
		SchemaHash:       snap.SchemaHash,
		DisplayOnline:    snap.DisplayOnline,
		ClientsOnline:    snap.ClientsOnline,
		CloudOnline:      snap.CloudOnline,
		Wifi:             snap.Wifi,
		PairingMode:      "code",
		ServerMs:         time.Now().UnixMilli(),
	}
	if s.devMode {
		resp.PairingCode = s.deviceAuth.PairCode()
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) handlePair(c *gin.Context) {
	var req PairRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, ckframework.ErrValidation.Wrap(err, "invalid pair payload"))
		return
	}
	if req.DeviceInfo.ProtocolVersion > 0 && req.DeviceInfo.ProtocolVersion != ckframework.ProtocolVersion {
		respondError(c, ckframework.ErrUpgradeRequired.New("client protocol %d is incompatible with server protocol %d", req.DeviceInfo.ProtocolVersion, ckframework.ProtocolVersion))
		return
	}
	resp, rec, err := s.deviceAuth.Pair(c.Request.Context(), req, c.ClientIP())
	if err != nil {
		respondError(c, err)
		return
	}
	if s.outbox != nil {
		_, _ = s.outbox.Record(c.Request.Context(), cktelemetry.Event{
			Category: "audit",
			Kind:     "device.paired",
			Severity: "info",
			Payload: map[string]any{
				"device_id":   rec.DeviceID,
				"device_name": rec.DeviceName,
				"device_type": rec.DeviceType,
				"model":       rec.Model,
				"os_name":     rec.OSName,
				"os_version":  rec.OSVersion,
				"app_version": rec.AppVersion,
				"public_key":  rec.PublicKey,
			},
		})
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) handleAuthChallenge(c *gin.Context) {
	nonce, sig := s.deviceAuth.IssueChallenge()
	c.JSON(http.StatusOK, gin.H{
		"nonce":      nonce,
		"server_id":  s.identity.ServerID,
		"server_pub": s.identity.PubBase64,
		"server_sig": sig,
	})
}

func (s *Server) handleAuthVerify(c *gin.Context) {
	var body struct {
		ClientID   string                  `json:"client_id"`
		Nonce      string                  `json:"nonce"`
		Sig        string                  `json:"sig"`
		DeviceInfo *ckframework.DeviceInfo `json:"device_info,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, ckframework.ErrValidation.Wrap(err, "invalid auth verify payload"))
		return
	}
	if body.DeviceInfo != nil && body.DeviceInfo.ProtocolVersion > 0 && body.DeviceInfo.ProtocolVersion != ckframework.ProtocolVersion {
		respondError(c, ckframework.ErrUpgradeRequired.New("client protocol mismatch"))
		return
	}
	rec, tok, err := s.deviceAuth.VerifyChallenge(c.Request.Context(), body.ClientID, body.Nonce, body.Sig, body.DeviceInfo, c.ClientIP())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"device_token": tok,
		"device":       rec,
		"snapshot":     s.hub.Snapshot(),
	})
}

func (s *Server) handleSchema(c *gin.Context) {
	c.JSON(http.StatusOK, s.hub.Schema())
}

func (s *Server) handleState(c *gin.Context) {
	c.JSON(http.StatusOK, s.hub.Snapshot())
}

func (s *Server) handleSubmitForm(armRound bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		dev := c.MustGet(ctxDeviceKey).(*ckframework.DeviceRecord)
		var body struct {
			RequestID string         `json:"request_id"`
			FormID    string         `json:"form_id"`
			Fields    map[string]any `json:"fields"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			respondError(c, ckframework.ErrValidation.Wrap(err, "invalid form submission body"))
			return
		}
		sub, round, err := s.hub.SubmitForm(c.Request.Context(), body.FormID, body.RequestID, body.Fields, dev, armRound)
		if err != nil {
			respondError(c, err)
			return
		}
		resp := gin.H{
			"submission_id": sub.ID,
			"request_id":    sub.RequestID,
			"form_id":       sub.FormID,
			"phase":         s.hub.Snapshot().Phase,
		}
		if round != nil {
			resp["round_id"] = round.ID
		}
		c.JSON(http.StatusAccepted, resp)
	}
}

func (s *Server) handleCancelRound(c *gin.Context) {
	active := s.hub.ActiveRound()
	if active == nil || active.ID != c.Param("id") {
		respondError(c, ckframework.ErrNotFound.New("round not active"))
		return
	}
	s.hub.CancelActiveRound(c.Request.Context(), "tablet_cancel")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleGetBoard(c *gin.Context) {
	boardID := c.Param("board_id")
	snap := s.hub.Snapshot()
	view, ok := snap.Boards[boardID]
	if !ok {
		respondError(c, ckframework.ErrNotFound.New("board %q not found", boardID))
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) handleOperatorLogin(c *gin.Context) {
	dev := c.MustGet(ctxDeviceKey).(*ckframework.DeviceRecord)
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, ckframework.ErrValidation.Wrap(err, "invalid login payload"))
		return
	}
	res, err := s.operatorAuth.Login(c.Request.Context(), body.Username, body.Password, dev.DeviceID)
	if err != nil {
		if s.outbox != nil {
			_, _ = s.outbox.Record(c.Request.Context(), cktelemetry.Event{
				Category: "audit",
				Kind:     "operator.login_failed",
				Severity: "warn",
				Payload: map[string]any{
					"username":  body.Username,
					"device_id": dev.DeviceID,
				},
			})
		}
		respondError(c, err)
		return
	}
	if s.outbox != nil {
		_, _ = s.outbox.Record(c.Request.Context(), cktelemetry.Event{
			Category: "audit",
			Kind:     "operator.login",
			Severity: "info",
			Payload: map[string]any{
				"operator_id": res.Operator.ID,
				"username":    res.Operator.Username,
				"device_id":   dev.DeviceID,
			},
		})
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleOperatorRefresh(c *gin.Context) {
	dev := c.MustGet(ctxDeviceKey).(*ckframework.DeviceRecord)
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)
	res, err := s.operatorAuth.Refresh(c.Request.Context(), op.OperatorID, dev.DeviceID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleGetConfig(c *gin.Context) {
	rev, vals, rtVals, settings := s.hub.ConfigState()
	c.JSON(http.StatusOK, gin.H{
		"revision":       rev,
		"values":         vals,
		"runtime_values": rtVals,
		"settings":       settings,
	})
}

func (s *Server) handlePatchConfig(c *gin.Context) {
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)
	var body struct {
		ExpectedRevision *int           `json:"expected_revision"`
		Values           map[string]any `json:"values"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Values == nil {
		respondError(c, ckframework.ErrValidation.New("values map is required"))
		return
	}
	expected := -1
	if body.ExpectedRevision != nil {
		expected = *body.ExpectedRevision
	}
	rev, vals, rtVals, changed, err := s.hub.ApplyConfigPatch(c.Request.Context(), expected, body.Values, op.Username)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"revision":       rev,
		"values":         vals,
		"runtime_values": rtVals,
		"changed":        changed,
	})
}

func (s *Server) handleTriggerAction(c *gin.Context) {
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)
	var body struct {
		Params map[string]any `json:"params"`
	}
	_ = c.ShouldBindJSON(&body)
	res, err := s.hub.TriggerAction(c.Request.Context(), c.Param("id"), body.Params, op)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleDeleteBoardRow(c *gin.Context) {
	op := c.MustGet(ctxOperatorKey).(*OperatorClaims)
	if err := s.hub.DeleteBoardRow(c.Request.Context(), c.Param("board_id"), c.Param("entry_id"), op.OperatorID); err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleListSubmissions(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	items, total, err := s.store.ListSubmissions(c.Request.Context(), c.Query("form_id"), c.Query("q"), limit, offset)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) handleExportSubmissionsCSV(c *gin.Context) {
	form, ok := s.spec.FormByID(c.Query("form_id"))
	if !ok {
		respondError(c, ckframework.ErrNotFound.New("form not found"))
		return
	}
	data, err := s.store.ExportSubmissionsCSV(c.Request.Context(), form)
	if err != nil {
		respondError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+s.spec.Name+`-`+form.ID+`.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(data))
}

func (s *Server) handleListDevices(c *gin.Context) {
	items, err := s.store.ListDevices(c.Request.Context(), c.Query("include_revoked") == "true")
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"devices": items})
}

func (s *Server) handleRevokeDevice(c *gin.Context) {
	if err := s.store.RevokeDevice(c.Request.Context(), c.Param("id"), time.Now().UnixMilli()); err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleListOperators(c *gin.Context) {
	items, err := s.store.ListOperators(c.Request.Context())
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"operators": items})
}

func (s *Server) handleCreateOperator(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, ckframework.ErrValidation.Wrap(err, "invalid operator payload"))
		return
	}
	rec, err := s.operatorAuth.CreateOperator(c.Request.Context(), body.Username, body.Password)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, rec)
}

func (s *Server) handleUpdateOperator(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, ckframework.ErrValidation.Wrap(err, "invalid update operator payload"))
		return
	}
	if err := s.operatorAuth.UpdateCredentials(c.Request.Context(), c.Param("id"), body.Username, body.Password); err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleDeleteOperator(c *gin.Context) {
	if err := s.store.DeleteOperator(c.Request.Context(), c.Param("id")); err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Bidirectional local-first telemetry relay handlers
func (s *Server) handleTelemetryIngest(c *gin.Context) {
	dev := c.MustGet(ctxDeviceKey).(*ckframework.DeviceRecord)
	var body struct {
		Events []cktelemetry.Event `json:"events"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, ckframework.ErrValidation.Wrap(err, "invalid telemetry ingest body"))
		return
	}
	accepted, err := s.outbox.IngestRelayed(c.Request.Context(), dev.DeviceID, body.Events)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"accepted_ids": accepted, "cloud_online": s.hub.CloudOnline()})
}

func (s *Server) handleTelemetryPull(c *gin.Context) {
	dev := c.MustGet(ctxDeviceKey).(*ckframework.DeviceRecord)
	if !dev.AllowCloudRelay {
		respondError(c, ckframework.ErrForbidden.New("device is not permitted to relay server telemetry"))
		return
	}
	afterSeq, _ := strconv.ParseInt(c.DefaultQuery("after_seq", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "200"))
	items, maxSeq, hasMore, err := s.outbox.PullUnsynced(c.Request.Context(), afterSeq, limit)
	if err != nil {
		respondError(c, err)
		return
	}
	_ = s.outbox.UpdatePeerCursor(c.Request.Context(), dev.DeviceID, maxSeq, 0)
	c.JSON(http.StatusOK, gin.H{
		"items":        items,
		"max_seq":      maxSeq,
		"has_more":     hasMore,
		"cloud_online": s.hub.CloudOnline(),
	})
}

func (s *Server) handleTelemetryAck(c *gin.Context) {
	dev := c.MustGet(ctxDeviceKey).(*ckframework.DeviceRecord)
	var body struct {
		SyncedEventIDs []string `json:"synced_event_ids"`
		UpToSeq        int64    `json:"up_to_seq"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, ckframework.ErrValidation.Wrap(err, "invalid telemetry ack body"))
		return
	}
	if err := s.outbox.MarkCloudSynced(c.Request.Context(), body.SyncedEventIDs, time.Now().UnixMilli()); err != nil {
		respondError(c, err)
		return
	}
	_ = s.outbox.UpdatePeerCursor(c.Request.Context(), dev.DeviceID, body.UpToSeq, body.UpToSeq)
	c.JSON(http.StatusOK, gin.H{"ok": true, "acked": len(body.SyncedEventIDs)})
}

// handleEventsWS performs mutual Ed25519 challenge-response auth on connect,
// then streams snapshots, config diffs, board updates, and handles live operator WS patches.
func (s *Server) handleEventsWS(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	defer conn.CloseNow()

	nonce, serverSig := s.deviceAuth.IssueChallenge()
	challengeBytes, _ := json.Marshal(gin.H{
		"t":          "challenge",
		"nonce":      nonce,
		"server_id":  s.identity.ServerID,
		"server_pub": s.identity.PubBase64,
		"server_sig": serverSig,
	})
	if err := conn.Write(ctx, websocket.MessageText, challengeBytes); err != nil {
		return
	}

	authCtx, authCancel := context.WithTimeout(ctx, 10*time.Second)
	_, data, err := conn.Read(authCtx)
	authCancel()
	if err != nil {
		return
	}
	var authMsg struct {
		T          string                  `json:"t"`
		ClientID   string                  `json:"client_id"`
		Sig        string                  `json:"sig"`
		DeviceInfo *ckframework.DeviceInfo `json:"device_info,omitempty"`
	}
	if err := json.Unmarshal(data, &authMsg); err != nil || authMsg.T != "auth" {
		_ = conn.Close(websocket.StatusPolicyViolation, "expected auth frame")
		return
	}
	if authMsg.DeviceInfo != nil && authMsg.DeviceInfo.ProtocolVersion > 0 && authMsg.DeviceInfo.ProtocolVersion != ckframework.ProtocolVersion {
		_ = conn.Close(4026, "protocol_version_mismatch")
		return
	}
	dev, devTok, err := s.deviceAuth.VerifyChallenge(ctx, authMsg.ClientID, nonce, authMsg.Sig, authMsg.DeviceInfo, c.ClientIP())
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}

	tc := &tabletConn{
		deviceID:   dev.DeviceID,
		deviceName: dev.DeviceName,
		send:       make(chan []byte, 64),
	}
	s.hub.addTablet(tc)
	defer s.hub.removeTablet(tc)

	rev, vals, rtVals, settings := s.hub.ConfigState()
	authOK, _ := json.Marshal(gin.H{
		"t":              "auth_ok",
		"device_token":   devTok,
		"snapshot":       s.hub.Snapshot(),
		"config_rev":     rev,
		"values":         vals,
		"runtime_values": rtVals,
		"settings":       settings,
	})
	if err := conn.Write(ctx, websocket.MessageText, authOK); err != nil {
		return
	}

	go func() {
		pingTick := time.NewTicker(10 * time.Second)
		defer pingTick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pingTick.C:
				pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			case payload, ok := <-tc.send:
				if !ok {
					return
				}
				wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Write(wctx, websocket.MessageText, payload)
				wcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var frame struct {
			T      string         `json:"t"`
			CID    string         `json:"cid"`
			Client float64        `json:"client_ms"`
			Token  string         `json:"token"`
			Values map[string]any `json:"values"`
		}
		if json.Unmarshal(raw, &frame) != nil {
			continue
		}
		switch frame.T {
		case "ping":
			pong, _ := json.Marshal(gin.H{
				"t":         "pong",
				"cid":       frame.CID,
				"client_ms": frame.Client,
				"server_ms": time.Now().UnixMilli(),
			})
			select {
			case tc.send <- pong:
			default:
			}
		case "operator_auth":
			claims, err := s.operatorAuth.VerifyToken(ctx, frame.Token, tc.deviceID)
			if err != nil {
				errMsg, _ := json.Marshal(gin.H{"t": "operator_auth_error", "error": err.Error()})
				tc.send <- errMsg
				continue
			}
			tc.setOperator(claims.OperatorID, claims.Username)
			okMsg, _ := json.Marshal(gin.H{"t": "operator_auth_ok", "username": claims.Username})
			tc.send <- okMsg
		case "operator_logout":
			tc.setOperator("", "")
		case "config_patch":
			opID, opUser := tc.operatorInfo()
			if opID == "" {
				errMsg, _ := json.Marshal(gin.H{"t": "error", "code": "operator_auth_required", "error": "operator login required"})
				tc.send <- errMsg
				continue
			}
			if _, _, _, _, err := s.hub.ApplyConfigPatch(ctx, -1, frame.Values, opUser); err != nil {
				errMsg, _ := json.Marshal(gin.H{"t": "error", "code": "invalid_config", "error": err.Error()})
				tc.send <- errMsg
			}
		}
	}
}
