package ckserver

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/ckserver/store"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/gin-gonic/gin"
)

type tabletConn struct {
	deviceID   string
	deviceName string
	send       chan []byte
	mu         sync.Mutex
	operatorID string
	operator   string
}

func (tc *tabletConn) setOperator(id, username string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.operatorID = id
	tc.operator = username
}

func (tc *tabletConn) operatorInfo() (string, string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.operatorID, tc.operator
}

type gameBridgeConn struct {
	send        chan []byte
	gameName    string
	gameVersion string
	engineVer   string
}

// ActionHandler handles server- or launcher-targeted ActionSpec invocations.
type ActionHandler func(ctx context.Context, params map[string]any, operator *OperatorClaims) (ckframework.ActionResult, error)

// Hub coordinates authoritative kiosk state, settings, boards, actions, and real-time WS fan-out.
type Hub struct {
	mu              sync.RWMutex
	spec            *ckframework.AppSpec
	store           *store.Store
	outbox          *cktelemetry.Outbox
	bootID          string
	serverID        string
	launcherVersion string
	gameVersion     string
	seq             int64
	phase           string
	configRev       int
	persistedValues map[string]any
	runtimeValues   map[string]any
	activeRound     *ckframework.ActiveRound
	boards          map[string]ckframework.BoardView
	catalogs        map[string]ckframework.CatalogView
	rulesets        map[string]ckframework.RulesetView

	// Dynamic game-declared capabilities merged into /v1/schema while game is connected.
	dynSettings []ckframework.SettingSpec
	dynActions  []ckframework.ActionSpec
	dynBoards   []ckframework.BoardSpec

	tablets          map[*tabletConn]struct{}
	game             *gameBridgeConn
	actionHandlers   map[string]ActionHandler
	actionCooldowns  map[string]time.Time
	pendingCalls     map[string]chan ckframework.ActionResult
	onConfigChanged  func(rev int, changed []string, values, runtime map[string]any)
	cloudOnlineFn    func() bool
	presenceInfoFn   func() (addr string, pairCode string)
	wifi             ckframework.WifiInfo
}

func NewHub(spec *ckframework.AppSpec, st *store.Store, outbox *cktelemetry.Outbox, serverID, bootID, launcherVersion, gameVersion string) (*Hub, error) {
	defP, defR := spec.DefaultSettings()
	rev, storedP, err := st.LoadSettings(context.Background())
	if err != nil {
		return nil, err
	}
	mergedP := maps.Clone(defP)
	for k, v := range storedP {
		if s, ok := spec.SettingByKey(k); ok && s.Scope == ckframework.ScopePersisted {
			mergedP[k] = v
		}
	}

	h := &Hub{
		spec:            spec,
		store:           st,
		outbox:          outbox,
		bootID:          bootID,
		serverID:        serverID,
		launcherVersion: launcherVersion,
		gameVersion:     gameVersion,
		phase:           ckframework.PhaseIdle,
		configRev:       rev,
		persistedValues: mergedP,
		runtimeValues:   defR,
		boards:          make(map[string]ckframework.BoardView),
		catalogs:        make(map[string]ckframework.CatalogView),
		rulesets:        make(map[string]ckframework.RulesetView),
		tablets:         make(map[*tabletConn]struct{}),
		actionHandlers:  make(map[string]ActionHandler),
		actionCooldowns: make(map[string]time.Time),
		pendingCalls:    make(map[string]chan ckframework.ActionResult),
	}

	// Register built-in server actions (board clears & force cancel).
	for _, b := range spec.Boards {
		if b.AllowBoardClear {
			boardCopy := b
			h.RegisterActionHandler("board."+boardCopy.ID+".clear", func(ctx context.Context, _ map[string]any, op *OperatorClaims) (ckframework.ActionResult, error) {
				opID := "launcher"
				if op != nil {
					opID = op.OperatorID
				}
				removed, err := h.ClearBoard(ctx, boardCopy.ID, opID)
				if err != nil {
					return ckframework.ActionResult{}, err
				}
				return ckframework.ActionResult{
					OK:      true,
					Action:  "board." + boardCopy.ID + ".clear",
					Message: "Cleared active rows while preserving historical submissions.",
					Data:    map[string]any{"removed_count": removed},
				}, nil
			})
		}
	}
	h.RegisterActionHandler("round.force_cancel", func(ctx context.Context, _ map[string]any, _ *OperatorClaims) (ckframework.ActionResult, error) {
		if !h.CancelActiveRound(ctx, "operator_cancel") {
			return ckframework.ActionResult{OK: false, Action: "round.force_cancel", Message: "No active round."}, nil
		}
		return ckframework.ActionResult{OK: true, Action: "round.force_cancel", Message: "Active round cancelled."}, nil
	})

	_ = h.RefreshAllBoards(context.Background())
	if err := h.initCatalogsAndRulesets(context.Background()); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Hub) SetCloudOnlineFunc(fn func() bool) {
	h.mu.Lock()
	h.cloudOnlineFn = fn
	h.mu.Unlock()
}

func (h *Hub) SetPresenceInfoFunc(fn func() (addr string, pairCode string)) {
	h.mu.Lock()
	h.presenceInfoFn = fn
	h.mu.Unlock()
}

func (h *Hub) presenceInfo() (string, string) {
	h.mu.RLock()
	fn := h.presenceInfoFn
	h.mu.RUnlock()
	if fn == nil {
		return "", ""
	}
	return fn()
}

// NotifyGamePresence pushes live LAN address, pairing code, and active companion client count
// to the connected game over /v1/bridge so the game can hide/show its pairing overlay accurately.
func (h *Hub) NotifyGamePresence() {
	addr, code := h.presenceInfo()
	h.mu.Lock()
	game := h.game
	clients := len(h.tablets)
	snap := h.snapshotLocked()
	h.mu.Unlock()
	if game != nil {
		h.sendToGame(game, ckframework.BridgeMessage{
			T:             "presence",
			Address:       addr,
			PairingCode:   code,
			ClientsOnline: &clients,
			Snapshot:      &snap,
		})
	}
}

func (h *Hub) SetOnConfigChanged(fn func(rev int, changed []string, values, runtime map[string]any)) {
	h.mu.Lock()
	h.onConfigChanged = fn
	h.mu.Unlock()
}

func (h *Hub) RegisterActionHandler(actionID string, fn ActionHandler) {
	h.mu.Lock()
	h.actionHandlers[actionID] = fn
	h.mu.Unlock()
}

func (h *Hub) allSettingsLocked() []ckframework.SettingSpec {
	out := slices.Clone(h.spec.Settings)
	out = append(out, h.dynSettings...)
	return out
}

func (h *Hub) allActionsLocked() []ckframework.ActionSpec {
	out := slices.Clone(h.spec.Actions)
	out = append(out, h.dynActions...)
	return out
}

func (h *Hub) allBoardsLocked() []ckframework.BoardSpec {
	out := slices.Clone(h.spec.Boards)
	out = append(out, h.dynBoards...)
	return out
}

func (h *Hub) Schema() ckframework.SchemaSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.schemaLocked()
}

func (h *Hub) schemaLocked() ckframework.SchemaSnapshot {
	snap := ckframework.SchemaSnapshot{
		AppName:          h.spec.Name,
		AppVersion:       h.spec.Version,
		FrameworkVersion: ckframework.Version,
		ProtocolVersion:  ckframework.ProtocolVersion,
		MinClientVersion: h.spec.MinClientVersion,
		Phases:           h.spec.Phases,
		Forms:            h.spec.Forms,
		Settings:         h.allSettingsLocked(),
		Actions:          h.allActionsLocked(),
		Boards:           h.allBoardsLocked(),
		Catalogs:         h.spec.Catalogs,
		Rulesets:         h.spec.Rulesets,
	}
	snap.SchemaHash = ckframework.ComputeSchemaHash(snap)
	return snap
}

func (h *Hub) Versions() ckframework.VersionsInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.versionsLocked()
}

func (h *Hub) versionsLocked() ckframework.VersionsInfo {
	gv := h.gameVersion
	if h.game != nil && h.game.gameVersion != "" {
		gv = h.game.gameVersion
	}
	return ckframework.VersionsInfo{
		App:       h.spec.Version,
		Framework: ckframework.Version,
		Launcher:  h.launcherVersion,
		Game:      gv,
		Protocol:  ckframework.ProtocolVersion,
	}
}

func (h *Hub) DisplayOnline() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.game != nil
}

func (h *Hub) CloudOnline() bool {
	h.mu.RLock()
	fn := h.cloudOnlineFn
	h.mu.RUnlock()
	if fn == nil {
		return false
	}
	return fn()
}

func (h *Hub) SetWifiInfo(info ckframework.WifiInfo) {
	h.mu.Lock()
	changed := h.wifi.Connected != info.Connected || h.wifi.SSID != info.SSID || h.wifi.BSSID != info.BSSID
	h.wifi = info
	var snap ckframework.Snapshot
	if changed {
		h.seq++
		snap = h.snapshotLocked()
	}
	h.mu.Unlock()
	if changed {
		h.broadcastTablets(snap)
	}
}

func (h *Hub) WifiInfo() ckframework.WifiInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.wifi
}

func (h *Hub) Snapshot() ckframework.Snapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.snapshotLocked()
}

func (h *Hub) snapshotLocked() ckframework.Snapshot {
	boardsCopy := make(map[string]ckframework.BoardView, len(h.boards))
	for k, v := range h.boards {
		boardsCopy[k] = v
	}
	catalogsCopy := make(map[string]ckframework.CatalogView, len(h.catalogs))
	for k, v := range h.catalogs {
		vCopy := v
		vCopy.Entries = ckframework.CloneCatalogEntries(v.Entries)
		catalogsCopy[k] = vCopy
	}
	rulesetsCopy := make(map[string]ckframework.RulesetView, len(h.rulesets))
	for k, v := range h.rulesets {
		vCopy := v
		vCopy.Rules = ckframework.CloneRules(v.Rules)
		rulesetsCopy[k] = vCopy
	}
	var roundCopy *ckframework.ActiveRound
	if h.activeRound != nil {
		rc := *h.activeRound
		if h.activeRound.Outcome != nil {
			rc.Outcome = maps.Clone(h.activeRound.Outcome)
		}
		if h.activeRound.Rewards != nil {
			rc.Rewards = maps.Clone(h.activeRound.Rewards)
		}
		roundCopy = &rc
	}
	cloudOnline := false
	if h.cloudOnlineFn != nil {
		cloudOnline = h.cloudOnlineFn()
	}
	var wifiPtr *ckframework.WifiInfo
	if h.wifi.Connected || h.wifi.SSID != "" {
		wc := h.wifi
		wifiPtr = &wc
	}
	return ckframework.Snapshot{
		Type:           "snapshot",
		BootID:         h.bootID,
		Seq:            h.seq,
		Phase:          h.phase,
		SchemaHash:     h.schemaLocked().SchemaHash,
		Versions:       h.versionsLocked(),
		DisplayOnline:  h.game != nil,
		ClientsOnline:  len(h.tablets),
		CloudOnline:    cloudOnline,
		Wifi:           wifiPtr,
		ConfigRevision: h.configRev,
		Round:          roundCopy,
		Boards:         boardsCopy,
		Catalogs:       catalogsCopy,
		Rulesets:       rulesetsCopy,
		ServerMs:       time.Now().UnixMilli(),
	}
}

func (h *Hub) ConfigState() (int, map[string]any, map[string]any, []ckframework.SettingSpec) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.configRev, maps.Clone(h.persistedValues), maps.Clone(h.runtimeValues), h.allSettingsLocked()
}

// ApplyConfigPatch validates, persists (if persisted settings changed), and broadcasts
// config updates in real time to Godot (/v1/bridge), all tablets (/v1/events), and cklauncher.
func (h *Hub) ApplyConfigPatch(ctx context.Context, expectedRev int, patch map[string]any, actor string) (int, map[string]any, map[string]any, []string, error) {
	h.mu.Lock()
	specs := h.allSettingsLocked()
	nextP, nextR, changed, persistedChanged, err := ckframework.ValidateConfigPatch(specs, h.persistedValues, h.runtimeValues, patch)
	if err != nil {
		h.mu.Unlock()
		return 0, nil, nil, nil, err
	}
	rev := h.configRev
	if persistedChanged {
		nextRev, err := h.store.SaveSettings(ctx, expectedRev, nextP, time.Now().UnixMilli())
		if err != nil {
			h.mu.Unlock()
			return nextRev, nil, nil, nil, err
		}
		rev = nextRev
	}
	h.configRev = rev
	h.persistedValues = nextP
	h.runtimeValues = nextR
	h.seq++
	cb := h.onConfigChanged
	game := h.game
	h.mu.Unlock()

	if h.outbox != nil && len(changed) > 0 {
		_, _ = h.outbox.Record(ctx, cktelemetry.Event{
			Category: "audit",
			Kind:     "config.changed",
			Severity: "info",
			Payload: map[string]any{
				"actor":    actor,
				"revision": rev,
				"changed":  changed,
				"patch":    patch,
			},
		})
	}

	if cb != nil {
		cb(rev, changed, maps.Clone(nextP), maps.Clone(nextR))
	}

	// Push live to Godot bridge.
	if game != nil {
		h.sendToGame(game, ckframework.BridgeMessage{
			T:             "config",
			Revision:      rev,
			Values:        nextP,
			RuntimeValues: nextR,
			Changed:       changed,
		})
	}

	// Push live to all connected tablets.
	h.broadcastTablets(gin.H{
		"type":           "config",
		"revision":       rev,
		"values":         nextP,
		"runtime_values": nextR,
		"changed":        changed,
		"settings":       specs,
	})

	// Refresh boards if a board limit setting changed.
	for _, b := range h.Schema().Boards {
		if b.LimitSettingKey != "" && slices.Contains(changed, b.LimitSettingKey) {
			_ = h.RefreshBoard(ctx, b.ID)
		}
	}

	return rev, maps.Clone(nextP), maps.Clone(nextR), changed, nil
}

// SubmitForm validates a form submission, atomically writes to immutable submissions
// (+ CKTelemetryTransmit outbox event), and optionally arms a round if armRound is true.
func (h *Hub) SubmitForm(ctx context.Context, formID, requestID string, rawFields map[string]any, sourceDevice *ckframework.DeviceRecord, armRound bool) (*ckframework.SubmissionRecord, *ckframework.ActiveRound, error) {
	if len(requestID) < 4 || len(requestID) > 64 {
		return nil, nil, ckframework.ErrValidation.New("request_id must be 4-64 characters")
	}
	form, ok := h.spec.FormByID(formID)
	if !ok {
		return nil, nil, ckframework.ErrNotFound.New("unknown form %q", formID)
	}
	cleaned, err := ckframework.ValidateFields(form.Fields, rawFields)
	if err != nil {
		return nil, nil, err
	}

	// Idempotent retry check.
	if existing, err := h.store.GetSubmissionByRequestID(ctx, requestID); err != nil {
		return nil, nil, err
	} else if existing != nil {
		return existing, h.ActiveRound(), nil
	}

	h.mu.Lock()
	if armRound {
		phaseSpec, _ := h.spec.PhaseByName(h.phase)
		if !phaseSpec.AllowSubmit {
			h.mu.Unlock()
			return nil, nil, ckframework.ErrBusy.New("round already in progress")
		}
		if h.game == nil {
			h.mu.Unlock()
			return nil, nil, ckframework.ErrBridgeUnavailable.New("display is offline")
		}
	}
	cfgRev := h.configRev
	h.mu.Unlock()

	now := time.Now().UnixMilli()
	subID := cktelemetry.NewEventID()
	roundID := ""
	var roundRec *store.RoundRecord
	var activeRound *ckframework.ActiveRound
	if armRound {
		roundID = "rnd_" + cktelemetry.NewEventID()
		roundRec = &store.RoundRecord{
			ID:             roundID,
			SubmissionID:   subID,
			RequestID:      requestID,
			FormID:         form.ID,
			State:          "pending",
			ConfigRevision: cfgRev,
			CreatedAtMs:    now,
		}
		activeRound = &ckframework.ActiveRound{
			ID:           roundID,
			SubmissionID: subID,
			RequestID:    requestID,
			FormID:       form.ID,
			Fields:       cleaned,
		}
	}

	sub := ckframework.SubmissionRecord{
		ID:             subID,
		RequestID:      requestID,
		FormID:         form.ID,
		FormVersion:    form.Version,
		Payload:        cleaned,
		SourceDeviceID: sourceDevice.DeviceID,
		SourceDevice:   sourceDevice.DeviceName,
		RoundID:        roundID,
		Status:         "submitted",
		CreatedAtMs:    now,
	}

	var telEv *cktelemetry.Event
	if form.TelemetryTransmit && h.outbox != nil {
		ev := cktelemetry.Event{
			EventID:        subID,
			OriginDeviceID: sourceDevice.DeviceID,
			OriginRole:     sourceDevice.Role,
			Category:       "form",
			Kind:           "form.submission",
			Severity:       "info",
			OccurredAtMs:   now,
			Payload: map[string]any{
				"submission_id":      subID,
				"form_id":            form.ID,
				"form_version":       form.Version,
				"form_schema":        form,
				"source_device_id":   sourceDevice.DeviceID,
				"source_device_name": sourceDevice.DeviceName,
				"round_id":           roundID,
				"status":             sub.Status,
				"fields":             cleaned,
			},
		}
		telEv = &ev
	}

	if err := h.store.CreateSubmissionWithOutbox(ctx, sub, roundRec, h.outbox, telEv); err != nil {
		return nil, nil, err
	}

	if armRound && activeRound != nil {
		h.mu.Lock()
		h.activeRound = activeRound
		h.phase = ckframework.PhaseAwaitingHand
		h.seq++
		game := h.game
		snap := h.snapshotLocked()
		h.mu.Unlock()

		if game != nil {
			h.sendToGame(game, ckframework.BridgeMessage{
				T:       "arm",
				RoundID: activeRound.ID,
				Round:   activeRound,
			})
		}
		h.broadcastTablets(snap)
	} else {
		// Non-blocking wall submission (e.g. ../esse wishing wall): forward to game immediately.
		h.mu.RLock()
		game := h.game
		h.mu.RUnlock()
		if game != nil {
			h.sendToGame(game, ckframework.BridgeMessage{
				T:            "submission",
				SubmissionID: sub.ID,
				Params:       cleaned,
			})
		}
	}

	return &sub, activeRound, nil
}

func (h *Hub) ActiveRound() *ckframework.ActiveRound {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.activeRound == nil {
		return nil
	}
	cp := *h.activeRound
	return &cp
}

func (h *Hub) CancelActiveRound(ctx context.Context, reason string) bool {
	h.mu.Lock()
	if h.activeRound == nil {
		h.mu.Unlock()
		return false
	}
	r := h.activeRound
	h.activeRound = nil
	h.phase = ckframework.PhaseIdle
	h.seq++
	game := h.game
	snap := h.snapshotLocked()
	h.mu.Unlock()

	_ = h.store.CancelRound(ctx, r.ID, r.SubmissionID, reason, time.Now().UnixMilli())
	if game != nil {
		h.sendToGame(game, ckframework.BridgeMessage{T: "cancel", RoundID: r.ID, Reason: reason})
	}
	h.broadcastTablets(snap)
	return true
}

// TriggerAction validates an ActionSpec and dispatches it to server, launcher, or game bridge.
func (h *Hub) TriggerAction(ctx context.Context, actionID string, rawParams map[string]any, op *OperatorClaims) (ckframework.ActionResult, error) {
	h.mu.Lock()
	var spec ckframework.ActionSpec
	found := false
	for _, a := range h.allActionsLocked() {
		if a.ID == actionID {
			spec = a
			found = true
			break
		}
	}
	if !found {
		h.mu.Unlock()
		return ckframework.ActionResult{}, ckframework.ErrNotFound.New("unknown action %q", actionID)
	}
	if len(spec.EnabledPhases) > 0 && !slices.Contains(spec.EnabledPhases, h.phase) {
		h.mu.Unlock()
		return ckframework.ActionResult{}, ckframework.ErrConflict.New("action %q is not available during phase %q", actionID, h.phase)
	}
	if spec.CooldownMs > 0 {
		if last, ok := h.actionCooldowns[actionID]; ok && time.Since(last) < time.Duration(spec.CooldownMs)*time.Millisecond {
			h.mu.Unlock()
			return ckframework.ActionResult{}, ckframework.ErrConflict.New("action %q is on cooldown", actionID)
		}
		h.actionCooldowns[actionID] = time.Now()
	}
	handler := h.actionHandlers[actionID]
	game := h.game
	h.mu.Unlock()

	params := map[string]any{}
	if len(spec.Params) > 0 {
		clean, err := ckframework.ValidateFields(spec.Params, rawParams)
		if err != nil {
			return ckframework.ActionResult{}, err
		}
		params = clean
	}

	var res ckframework.ActionResult
	var err error
	switch spec.Target {
	case ckframework.TargetGame:
		if game == nil {
			return ckframework.ActionResult{}, ckframework.ErrBridgeUnavailable.New("game display is offline")
		}
		res, err = h.dispatchGameAction(ctx, game, actionID, params)
	default:
		if handler == nil {
			return ckframework.ActionResult{}, ckframework.ErrNotFound.New("no handler registered for action %q", actionID)
		}
		res, err = handler(ctx, params, op)
	}

	if h.outbox != nil {
		actor := "launcher"
		if op != nil {
			actor = op.Username
		}
		_, _ = h.outbox.Record(ctx, cktelemetry.Event{
			Category: "audit",
			Kind:     "action.triggered",
			Severity: "info",
			Payload: map[string]any{
				"action_id": actionID,
				"target":    spec.Target,
				"actor":     actor,
				"ok":        err == nil && res.OK,
				"message":   res.Message,
			},
		})
	}
	return res, err
}

func (h *Hub) dispatchGameAction(ctx context.Context, game *gameBridgeConn, actionID string, params map[string]any) (ckframework.ActionResult, error) {
	callID := "act_" + cktelemetry.NewEventID()
	ch := make(chan ckframework.ActionResult, 1)
	h.mu.Lock()
	h.pendingCalls[callID] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pendingCalls, callID)
		h.mu.Unlock()
	}()

	h.sendToGame(game, ckframework.BridgeMessage{
		T:        "action",
		CallID:   callID,
		ActionID: actionID,
		Params:   params,
	})

	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case res := <-ch:
		return res, nil
	case <-tctx.Done():
		return ckframework.ActionResult{}, ckframework.ErrBridgeTimeout.New("game did not acknowledge action %q in time", actionID)
	}
}

func (h *Hub) RefreshAllBoards(ctx context.Context) error {
	for _, b := range h.Schema().Boards {
		if err := h.RefreshBoard(ctx, b.ID); err != nil {
			return err
		}
	}
	return nil
}

func (h *Hub) RefreshBoard(ctx context.Context, boardID string) error {
	h.mu.RLock()
	var spec ckframework.BoardSpec
	found := false
	for _, b := range h.allBoardsLocked() {
		if b.ID == boardID {
			spec = b
			found = true
			break
		}
	}
	limit := spec.Limit
	if spec.LimitSettingKey != "" {
		if v, ok := h.persistedValues[spec.LimitSettingKey]; ok {
			switch n := v.(type) {
			case int:
				limit = n
			case float64:
				limit = int(n)
			}
		}
	}
	game := h.game
	h.mu.RUnlock()
	if !found {
		return ckframework.ErrNotFound.New("unknown board %q", boardID)
	}

	rows, err := h.store.QueryBoard(ctx, spec, limit)
	if err != nil {
		return err
	}
	view := ckframework.BoardView{
		BoardID:     spec.ID,
		Title:       spec.Title,
		Rows:        rows,
		UpdatedAtMs: time.Now().UnixMilli(),
	}
	h.mu.Lock()
	h.boards[spec.ID] = view
	h.seq++
	h.mu.Unlock()

	h.broadcastTablets(gin.H{
		"type":     "board_updated",
		"board_id": view.BoardID,
		"board":    view,
	})
	if game != nil {
		h.sendToGame(game, ckframework.BridgeMessage{
			T:       "board_snapshot",
			BoardID: view.BoardID,
			Rows:    view.Rows,
		})
	}
	return nil
}

// DeleteBoardRow soft-tombstones a board entry (never edits contents, never deletes submission),
// records a 2P-Set CRDT telemetry event, and notifies Godot + all tablets live.
func (h *Hub) DeleteBoardRow(ctx context.Context, boardID, entryID, operatorID string) error {
	var spec ckframework.BoardSpec
	found := false
	for _, b := range h.Schema().Boards {
		if b.ID == boardID {
			spec = b
			found = true
			break
		}
	}
	if !found {
		return ckframework.ErrNotFound.New("unknown board %q", boardID)
	}
	if !spec.AllowRowDelete {
		return ckframework.ErrForbidden.New("deleting rows is disabled on board %q", boardID)
	}
	now := time.Now().UnixMilli()
	if err := h.store.RemoveBoardEntry(ctx, boardID, entryID, operatorID, now); err != nil {
		return err
	}
	if h.outbox != nil {
		_, _ = h.outbox.Record(ctx, cktelemetry.Event{
			Category: "board",
			Kind:     "board.entry_removed",
			Severity: "info",
			Payload: map[string]any{
				"board_id":               boardID,
				"entry_id":               entryID,
				"removed_at_ms":          now,
				"removed_by_operator_id": operatorID,
			},
		})
	}
	h.mu.RLock()
	game := h.game
	h.mu.RUnlock()
	if game != nil {
		h.sendToGame(game, ckframework.BridgeMessage{
			T:       "board_row_removed",
			BoardID: boardID,
			EntryID: entryID,
		})
	}
	return h.RefreshBoard(ctx, boardID)
}

func (h *Hub) ClearBoard(ctx context.Context, boardID, operatorID string) (int64, error) {
	now := time.Now().UnixMilli()
	n, err := h.store.ClearBoard(ctx, boardID, operatorID, now)
	if err != nil {
		return 0, err
	}
	if h.outbox != nil {
		_, _ = h.outbox.Record(ctx, cktelemetry.Event{
			Category: "board",
			Kind:     "board.cleared",
			Severity: "warn",
			Payload: map[string]any{
				"board_id":               boardID,
				"removed_count":          n,
				"removed_by_operator_id": operatorID,
			},
		})
	}
	_ = h.RefreshBoard(ctx, boardID)
	return n, nil
}

func (h *Hub) addTablet(tc *tabletConn) {
	addr, code := h.presenceInfo()
	h.mu.Lock()
	h.tablets[tc] = struct{}{}
	h.seq++
	clients := len(h.tablets)
	game := h.game
	snap := h.snapshotLocked()
	h.mu.Unlock()

	if game != nil {
		h.sendToGame(game, ckframework.BridgeMessage{
			T:             "presence",
			Address:       addr,
			PairingCode:   code,
			ClientsOnline: &clients,
			Snapshot:      &snap,
		})
	}
	h.broadcastTablets(snap)
}

func (h *Hub) removeTablet(tc *tabletConn) {
	addr, code := h.presenceInfo()
	h.mu.Lock()
	delete(h.tablets, tc)
	h.seq++
	clients := len(h.tablets)
	game := h.game
	snap := h.snapshotLocked()
	h.mu.Unlock()

	if game != nil {
		h.sendToGame(game, ckframework.BridgeMessage{
			T:             "presence",
			Address:       addr,
			PairingCode:   code,
			ClientsOnline: &clients,
			Snapshot:      &snap,
		})
	}
	h.broadcastTablets(snap)
}

func (h *Hub) broadcastTablets(msg any) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for tc := range h.tablets {
		select {
		case tc.send <- raw:
		default:
		}
	}
}

func (h *Hub) sendToGame(game *gameBridgeConn, msg ckframework.BridgeMessage) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case game.send <- raw:
	default:
	}
}
