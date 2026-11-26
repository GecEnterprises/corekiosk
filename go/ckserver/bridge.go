package ckserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleBridge(c *gin.Context) {
	if s.bridgeToken != "" && c.Query("token") != s.bridgeToken && c.GetHeader("X-Bridge-Token") != s.bridgeToken {
		respondError(c, ckframework.ErrUnauthorized.New("invalid bridge token"))
		return
	}
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	gc := &gameBridgeConn{send: make(chan []byte, 64)}
	s.hub.mu.Lock()
	s.hub.game = gc
	s.hub.seq++
	snap := s.hub.snapshotLocked()
	s.hub.mu.Unlock()
	s.hub.broadcastTablets(snap)

	if s.outbox != nil {
		_, _ = s.outbox.Record(ctx, cktelemetry.Event{
			Category: "ws",
			Kind:     "bridge.connected",
			Severity: "info",
		})
	}

	defer func() {
		s.hub.mu.Lock()
		if s.hub.game == gc {
			s.hub.game = nil
			s.hub.dynSettings = nil
			s.hub.dynActions = nil
			s.hub.dynBoards = nil
			s.hub.seq++
		}
		snap := s.hub.snapshotLocked()
		s.hub.mu.Unlock()
		s.hub.broadcastTablets(snap)
		if s.outbox != nil {
			_, _ = s.outbox.Record(context.Background(), cktelemetry.Event{
				Category: "ws",
				Kind:     "bridge.disconnected",
				Severity: "warn",
			})
		}
		_ = conn.CloseNow()
	}()

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
			case payload, ok := <-gc.send:
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
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg ckframework.BridgeMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		s.handleBridgeMessage(ctx, gc, msg)
	}
}

func (s *Server) handleBridgeMessage(ctx context.Context, gc *gameBridgeConn, msg ckframework.BridgeMessage) {
	switch msg.T {
	case "ready":
		if msg.BridgeProtocol > 0 && msg.BridgeProtocol != ckframework.ProtocolVersion {
			s.hub.sendToGame(gc, ckframework.BridgeMessage{
				T:       "error",
				Message: "bridge protocol version mismatch",
			})
			return
		}
		s.hub.mu.Lock()
		gc.gameName = msg.GameName
		if msg.GameVersion != "" {
			gc.gameVersion = msg.GameVersion
			s.hub.gameVersion = msg.GameVersion
		}
		gc.engineVer = msg.EngineVersion
		if msg.Capabilities != nil {
			s.hub.dynSettings = msg.Capabilities.Settings
			s.hub.dynActions = msg.Capabilities.Actions
			s.hub.dynBoards = msg.Capabilities.Boards
			for _, st := range msg.Capabilities.Settings {
				if st.Scope == ckframework.ScopeRuntime {
					if _, exists := s.hub.runtimeValues[st.Key]; !exists {
						s.hub.runtimeValues[st.Key] = st.Default
					}
				}
			}
		}
		rev := s.hub.configRev
		vals := s.hub.persistedValues
		rtVals := s.hub.runtimeValues
		vers := s.hub.versionsLocked()
		clients := len(s.hub.tablets)
		snap := s.hub.snapshotLocked()
		s.hub.mu.Unlock()

		addr, code := s.hub.presenceInfo()
		cats := s.hub.Catalogs()
		rules := s.hub.Rulesets()
		s.hub.sendToGame(gc, ckframework.BridgeMessage{
			T:             "hello",
			Revision:      rev,
			Values:        vals,
			RuntimeValues: rtVals,
			Catalogs:      cats,
			Rulesets:      rules,
			Versions:      &vers,
			Snapshot:      &snap,
			Address:       addr,
			PairingCode:   code,
			ClientsOnline: &clients,
		})
		_ = s.hub.RefreshAllBoards(ctx)
		s.hub.broadcastTablets(snap)

	case "phase", "awaiting_hand", "countdown", "playing":
		nextPhase := msg.Phase
		if nextPhase == "" {
			nextPhase = msg.T
		}
		s.hub.mu.Lock()
		s.hub.phase = nextPhase
		if nextPhase == ckframework.PhasePlaying && s.hub.activeRound != nil {
			now := time.Now().UnixMilli()
			s.hub.activeRound.StartedAtMs = now
			_ = s.store.UpdateRoundState(ctx, s.hub.activeRound.ID, "live", now)
		}
		s.hub.seq++
		snap := s.hub.snapshotLocked()
		s.hub.mu.Unlock()
		s.hub.broadcastTablets(snap)

	case "score":
		s.hub.mu.Lock()
		if s.hub.activeRound != nil {
			s.hub.activeRound.Score = msg.Score
			s.hub.seq++
		}
		snap := s.hub.snapshotLocked()
		s.hub.mu.Unlock()
		s.hub.broadcastTablets(snap)

	case "finished":
		s.hub.mu.RLock()
		var roundFields map[string]any
		if s.hub.activeRound != nil {
			roundFields = s.hub.activeRound.Fields
		}
		s.hub.mu.RUnlock()

		outcome, rewards := s.hub.EvaluateRoundOutcome(msg.Score, msg.Reason, roundFields)

		s.hub.mu.Lock()
		round := s.hub.activeRound
		if round != nil {
			round.Score = msg.Score
			round.Outcome = outcome
			if len(rewards) > 0 {
				round.Rewards = rewards
			}
		}
		s.hub.phase = ckframework.PhaseResults
		s.hub.seq++
		s.hub.mu.Unlock()

		now := time.Now().UnixMilli()
		if round != nil {
			_ = s.store.FinishRoundWithOutcome(ctx, round.ID, round.SubmissionID, msg.Score, msg.Reason, outcome, now)
			// Emit form.outcome telemetry so Firebase table row gets the final score and resolved reward.
			if s.outbox != nil {
				_, _ = s.outbox.Record(ctx, cktelemetry.Event{
					Category: "form",
					Kind:     "form.outcome",
					Severity: "info",
					Payload: map[string]any{
						"form_id":       round.FormID,
						"submission_id": round.SubmissionID,
						"round_id":      round.ID,
						"status":        "played",
						"outcome":       outcome,
					},
				})
			}
			// Populate server-defined leaderboards automatically.
			for _, b := range s.hub.Schema().Boards {
				if b.Source != ckframework.BoardSourceServer {
					continue
				}
				if b.MinScore != nil && float64(msg.Score) < *b.MinScore {
					continue
				}
				display := map[string]any{"score": msg.Score}
				for k, v := range round.Fields {
					display[k] = v
				}
				if rt, ok := outcome["reward_title"]; ok && rt != "" {
					display["reward"] = rt
				}
				_ = s.store.UpsertBoardEntry(ctx, b, round.ID, round.SubmissionID, float64(msg.Score), display, now)
				if s.outbox != nil {
					_, _ = s.outbox.Record(ctx, cktelemetry.Event{
						Category: "board",
						Kind:     "board.entry_added",
						Severity: "info",
						Payload: map[string]any{
							"board_id":      b.ID,
							"entry_id":      round.ID,
							"submission_id": round.SubmissionID,
							"sort_value":    float64(msg.Score),
							"display":       display,
							"created_at_ms": now,
						},
					})
				}
				_ = s.hub.RefreshBoard(ctx, b.ID)
			}
		}
		snap := s.hub.Snapshot()
		if len(s.hub.spec.Rulesets) > 0 {
			var roundID string
			if round != nil {
				roundID = round.ID
			}
			s.hub.sendToGame(gc, ckframework.BridgeMessage{
				T:        "results",
				RoundID:  roundID,
				Score:    msg.Score,
				Outcome:  outcome,
				Rewards:  rewards,
				Snapshot: &snap,
			})
		}
		s.hub.broadcastTablets(snap)

	case "idle":
		s.hub.mu.Lock()
		s.hub.activeRound = nil
		s.hub.phase = ckframework.PhaseIdle
		s.hub.seq++
		snap := s.hub.snapshotLocked()
		s.hub.mu.Unlock()
		s.hub.broadcastTablets(snap)

	case "cancelled":
		s.hub.CancelActiveRound(ctx, msg.Reason)

	case "config_patch":
		if len(msg.Values) > 0 {
			_, _, _, _, _ = s.hub.ApplyConfigPatch(ctx, -1, msg.Values, "game_bridge")
		}

	case "action_result":
		s.hub.mu.RLock()
		ch := s.hub.pendingCalls[msg.CallID]
		h := s.hub
		h.mu.RUnlock()
		if ch != nil {
			select {
			case ch <- ckframework.ActionResult{OK: msg.OK, Action: msg.ActionID, Message: msg.Message, Data: msg.Data}:
			default:
			}
		}

	case "board_define":
		if msg.Board != nil && msg.Board.ID != "" {
			s.hub.mu.Lock()
			updated := false
			for i, b := range s.hub.dynBoards {
				if b.ID == msg.Board.ID {
					s.hub.dynBoards[i] = *msg.Board
					updated = true
					break
				}
			}
			if !updated {
				s.hub.dynBoards = append(s.hub.dynBoards, *msg.Board)
			}
			s.hub.mu.Unlock()
			_ = s.hub.RefreshBoard(ctx, msg.Board.ID)
			s.hub.broadcastTablets(s.hub.Snapshot())
		}

	case "board_upsert":
		for _, b := range s.hub.Schema().Boards {
			if b.ID == msg.BoardID {
				now := time.Now().UnixMilli()
				entryID := msg.EntryID
				if entryID == "" {
					entryID = cktelemetry.NewEventID()
				}
				_ = s.store.UpsertBoardEntry(ctx, b, entryID, msg.SubmissionID, msg.SortValue, msg.Display, now)
				if s.outbox != nil {
					_, _ = s.outbox.Record(ctx, cktelemetry.Event{
						Category: "board",
						Kind:     "board.entry_added",
						Severity: "info",
						Payload: map[string]any{
							"board_id":      b.ID,
							"entry_id":      entryID,
							"submission_id": msg.SubmissionID,
							"sort_value":    msg.SortValue,
							"display":       msg.Display,
							"created_at_ms": now,
						},
					})
				}
				_ = s.hub.RefreshBoard(ctx, b.ID)
				break
			}
		}
	}
}

// Unused import safeguard for net/http status constants if needed by callers.
var _ = http.StatusOK
