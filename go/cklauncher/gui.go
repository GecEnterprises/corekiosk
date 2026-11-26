package cklauncher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/ckserver"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
	"github.com/golang-gui/goui/ui"
)

type launcherUIState struct {
	srv     *ckserver.Server
	cd      *Countdown
	sup     *Supervisor
	lanAddr string

	activeTab    ui.State[string]
	statusMsg    ui.State[string]
	tick         ui.State[int]
	newOpUser    ui.State[string]
	newOpPass    ui.State[string]
	telRole      ui.State[string]
	telDeviceID  ui.State[string]
	telCategory  ui.State[string]
	telSync      ui.State[string]
	telSearch    ui.State[string]
	formSearch   ui.State[string]
	settingDraft map[string]*ui.State[string]
}

func newLauncherUIState(srv *ckserver.Server, cd *Countdown, sup *Supervisor, lanAddr string) *launcherUIState {
	st := &launcherUIState{
		srv:          srv,
		cd:           cd,
		sup:          sup,
		lanAddr:      lanAddr,
		activeTab:    ui.MakeState("overview"),
		statusMsg:    ui.MakeState("Ready"),
		tick:         ui.MakeState(0),
		newOpUser:    ui.MakeState(""),
		newOpPass:    ui.MakeState(""),
		telRole:      ui.MakeState("all"),
		telDeviceID:  ui.MakeState(""),
		telCategory:  ui.MakeState("all"),
		telSync:      ui.MakeState("all"),
		telSearch:    ui.MakeState(""),
		formSearch:   ui.MakeState(""),
		settingDraft: make(map[string]*ui.State[string]),
	}
	_, vals, rtVals, settings := srv.Hub().ConfigState()
	for _, spec := range settings {
		var v any
		if spec.Scope == ckframework.ScopeRuntime {
			v = rtVals[spec.Key]
		} else {
			v = vals[spec.Key]
		}
		s := ui.MakeState(fmt.Sprint(v))
		st.settingDraft[spec.Key] = &s
	}
	return st
}

// RunNativeUI launches the pure-Go native desktop window using github.com/golang-gui/goui.
func RunNativeUI(ctx context.Context, srv *ckserver.Server, cd *Countdown, sup *Supervisor, lanAddr string) error {
	st := newLauncherUIState(srv, cd, sup, lanAddr)
	appID := "org.gecenterprises.corekiosk." + sanitizeID(srv.Spec().Name)

	var timer *ui.Timer
	return ui.Run(appID, func(app ui.App) ui.RootView {
		if timer == nil {
			timer = app.TickFunc(1*time.Second, func() {
				if ctx.Err() != nil {
					app.Quit()
					return
				}
				st.tick.Update(func(n int) int { return n + 1 })
			})
			go func() {
				<-ctx.Done()
				app.Post(func() { app.Quit() })
			}()
		}

		return ui.Window("main").
			Title(fmt.Sprintf("%s — corekiosk Launcher & Telemetry Explorer", srv.Spec().Name)).
			Size(1240, 820).
			MinSize(960, 640).
			Center(true).
			OnDestroy(func() {
				if timer != nil {
					timer.Stop()
				}
				sup.Stop()
			}).
			Content(st.buildRoot(app))
	})
}

func (s *launcherUIState) buildRoot(app ui.App) ui.View {
	return ui.VBox(
		s.buildHeader(app),
		s.buildCountdownBar(app),
		s.buildTabBar(app),
		ui.VBox(
			ui.Build(func() ui.View {
				_ = s.tick.Get()
				tab := s.activeTab.Get()
				switch tab {
				case "controls":
					return s.buildControlsTab(app)
				case "telemetry":
					return s.buildTelemetryTab(app)
				case "forms":
					return s.buildFormsTab(app)
				case "boards":
					return s.buildBoardsTab(app)
				case "devices":
					return s.buildDevicesTab(app)
				default:
					return s.buildOverviewTab(app)
				}
			}),
		).MainWeight(1),
		ui.BorderBox(
			ui.HBox(
				ui.Label("Status:").Style("muted"),
				ui.Label("").BindText(&s.statusMsg).MainWeight(1),
			).Spacing(8).Padding(8),
		),
	).Spacing(10).Padding(14)
}

func (s *launcherUIState) buildHeader(app ui.App) ui.View {
	return ui.Build(func() ui.View {
		_ = s.tick.Get()
		v := s.srv.Hub().Versions()
		pending, _ := s.srv.Outbox().PendingCount(context.Background())
		bridgeStr := "OFFLINE"
		if s.srv.Hub().DisplayOnline() {
			bridgeStr = "ONLINE"
		}
		cloudStr := fmt.Sprintf("OFFLINE (%d queued)", pending)
		if s.srv.Shipper().CloudOnline() {
			cloudStr = fmt.Sprintf("ONLINE (%d queued)", pending)
		}

		return ui.BorderBox(
			ui.VBox(
				ui.HBox(
					ui.Label(fmt.Sprintf("%s  ·  corekiosk Launcher", s.srv.Spec().Name)).MainWeight(1),
					ui.Label(fmt.Sprintf("LAN: http://%s  |  Pair: %s  |  Bridge: %s  |  Firebase: %s",
						s.lanAddr, s.srv.DeviceAuth().PairCode(), bridgeStr, cloudStr)).Selectable(true),
					ui.Button("Force Cloud Sync").OnClick(func() {
						go func() {
							n, err := s.srv.Shipper().FlushNow(context.Background())
							app.Post(func() {
								if err != nil {
									s.statusMsg.Set("Cloud sync failed: " + err.Error())
								} else {
									s.statusMsg.Set(fmt.Sprintf("Synced %d event(s) to Firebase", n))
								}
								s.tick.Update(func(x int) int { return x + 1 })
							})
						}()
					}),
				).Spacing(10),
				ui.Label(fmt.Sprintf("Framework v%s  ·  Launcher v%s  ·  App v%s  ·  Game v%s  ·  Protocol %d  ·  Server Key: %s",
					v.Framework, v.Launcher, v.App, nonEmpty(v.Game, "-"), v.Protocol, s.srv.Identity().ServerID)).Selectable(true),
			).Spacing(6).Padding(10),
		)
	})
}

func (s *launcherUIState) buildCountdownBar(app ui.App) ui.View {
	return ui.Build(func() ui.View {
		_ = s.tick.Get()
		cd := s.cd.State()
		sup := s.sup.Status()

		var headline string
		switch {
		case cd.Disabled:
			headline = "Server-only mode (--no-game)"
		case cd.Launched && sup.Running:
			headline = fmt.Sprintf("Game Running (PID %d · restarts: %d · %s)", sup.PID, sup.RestartCount, sup.Binary)
		case cd.Launched:
			headline = fmt.Sprintf("Game Stopped (last exit %d · restarts: %d)", sup.LastExitCode, sup.RestartCount)
		case cd.Paused:
			headline = fmt.Sprintf("Pre-launch countdown PAUSED at %ds", cd.RemainingSeconds)
		default:
			headline = fmt.Sprintf("Launching game in %ds…", cd.RemainingSeconds)
		}

		pauseLabel := "Pause Countdown"
		if cd.Paused {
			pauseLabel = "Resume Countdown"
		}

		return ui.BorderBox(
			ui.HBox(
				ui.Label(headline).MainWeight(1),
				ui.Button(pauseLabel).Enabled(!cd.Disabled && !cd.Launched).OnClick(func() {
					if cd.Paused {
						s.cd.Resume()
						s.statusMsg.Set("Countdown resumed")
					} else {
						s.cd.Pause()
						s.statusMsg.Set("Countdown paused")
					}
					s.tick.Update(func(x int) int { return x + 1 })
				}),
				ui.Button("Launch Now").Enabled(!cd.Disabled && !cd.Launched).OnClick(func() {
					s.cd.LaunchNow()
					s.statusMsg.Set("Launching game now…")
					s.tick.Update(func(x int) int { return x + 1 })
				}),
				ui.Button("Restart Game").Enabled(!cd.Disabled).OnClick(func() {
					s.cd.LaunchNow()
					s.sup.Restart()
					s.statusMsg.Set("Game restart triggered")
					s.tick.Update(func(x int) int { return x + 1 })
				}),
				ui.Button("Stop Game").Enabled(sup.Running).OnClick(func() {
					s.sup.Stop()
					s.statusMsg.Set("Game stopped")
					s.tick.Update(func(x int) int { return x + 1 })
				}),
			).Spacing(8).Padding(10),
		)
	})
}

func (s *launcherUIState) buildTabBar(_ ui.App) ui.View {
	tabs := []struct {
		id    string
		label string
	}{
		{"overview", "Overview & Operator"},
		{"controls", "Controls & Actions"},
		{"telemetry", "Telemetry Explorer"},
		{"forms", "Forms Table"},
		{"boards", "Boards"},
		{"devices", "Paired Devices"},
	}
	return ui.Build(func() ui.View {
		cur := s.activeTab.Get()
		btns := make([]ui.View, 0, len(tabs))
		for _, t := range tabs {
			tabID := t.id
			label := t.label
			if cur == tabID {
				label = "[ " + label + " ]"
			}
			btns = append(btns, ui.Button(label).OnClick(func() {
				s.activeTab.Set(tabID)
			}))
		}
		return ui.ButtonGroup(btns...)
	})
}

func (s *launcherUIState) buildOverviewTab(_ ui.App) ui.View {
	ctx := context.Background()
	hint := s.srv.OperatorAuth().BoothHint(ctx)
	pending, _ := s.srv.Outbox().PendingCount(ctx)

	passDesc := "Custom password configured"
	if hint.IsDefault {
		passDesc = fmt.Sprintf("%s  (default — change below for production booth)", hint.DefaultPassword)
	}

	return ui.ScrollView(
		ui.VBox(
			ui.BorderBox(
				ui.VBox(
					ui.Label("BOOTH CONNECTION & PAIRING"),
					ui.Label(fmt.Sprintf("LAN URL: http://%s", s.lanAddr)).Selectable(true),
					ui.Label(fmt.Sprintf("Server Key ID: %s", s.srv.Identity().ServerID)).Selectable(true),
					ui.Label(fmt.Sprintf("6-Digit Pairing Code: %s", s.srv.DeviceAuth().PairCode())).Selectable(true),
					ui.HBox(
						ui.Button("Rotate Pairing Code").OnClick(func() {
							code := s.srv.DeviceAuth().RotatePairCode()
							s.statusMsg.Set("Rotated pairing code to " + code)
							s.tick.Update(func(x int) int { return x + 1 })
						}),
					),
				).Spacing(6).Padding(12),
			),
			ui.BorderBox(
				ui.VBox(
					ui.Label("BOOTH OPERATOR CREDENTIALS (TABLET OPERATOR PANEL LOGIN)"),
					ui.Label(fmt.Sprintf("Username: %s", hint.Username)).Selectable(true),
					ui.Label(fmt.Sprintf("Password: %s", passDesc)).Selectable(true),
					ui.HBox(
						ui.TextInput().BindText(&s.newOpUser).Placeholder("New username (blank to keep)").MainWeight(1),
						ui.TextInput().BindText(&s.newOpPass).Obscured(true).Placeholder("New password (min 4 chars)").MainWeight(1),
						ui.Button("Update Operator").OnClick(func() {
							ops, err := s.srv.Store().ListOperators(context.Background())
							if err != nil || len(ops) == 0 {
								s.statusMsg.Set("Failed to load operator record")
								return
							}
							u := strings.TrimSpace(s.newOpUser.Peek())
							p := s.newOpPass.Peek()
							if u == "" && p == "" {
								s.statusMsg.Set("Enter a new username or password first")
								return
							}
							if err := s.srv.OperatorAuth().UpdateCredentials(context.Background(), ops[0].ID, u, p); err != nil {
								s.statusMsg.Set("Update failed: " + err.Error())
								return
							}
							s.newOpUser.Set("")
							s.newOpPass.Set("")
							s.statusMsg.Set("Operator credentials updated (active tablet operator JWTs invalidated)")
							s.tick.Update(func(x int) int { return x + 1 })
						}),
					).Spacing(8),
				).Spacing(8).Padding(12),
			),
			ui.BorderBox(
				ui.VBox(
					ui.Label("LOCAL-FIRST TELEMETRY OUTBOX"),
					ui.Label(fmt.Sprintf("Pending Firebase Upload: %d event(s)", pending)),
					ui.Label(fmt.Sprintf("Last Sync Error: %s", nonEmpty(s.srv.Shipper().LastError(), "None"))),
				).Spacing(6).Padding(12),
			),
		).Spacing(12).Padding(4),
	).MainWeight(1)
}

func (s *launcherUIState) buildControlsTab(_ ui.App) ui.View {
	rev, vals, rtVals, settings := s.srv.Hub().ConfigState()
	schema := s.srv.Hub().Schema()

	settingRows := []ui.View{
		ui.Label(fmt.Sprintf("SETTINGS (revision %d — mirrors live to Godot & all paired tablets)", rev)),
	}
	for _, spec := range settings {
		sp := spec
		var curVal any
		if sp.Scope == ckframework.ScopeRuntime {
			curVal = rtVals[sp.Key]
		} else {
			curVal = vals[sp.Key]
		}
		meta := fmt.Sprintf("%s  [%s · %s]", sp.Label, sp.Scope, sp.Apply)

		switch sp.Type {
		case ckframework.SettingBool:
			b, _ := curVal.(bool)
			btnText := "OFF"
			if b {
				btnText = "ON"
			}
			settingRows = append(settingRows, ui.HBox(
				ui.Label(meta).MainWeight(1),
				ui.Button(btnText).OnClick(func() {
					s.applySetting(sp.Key, !b)
				}),
			).Spacing(8))

		case ckframework.SettingEnum:
			curStr := fmt.Sprint(curVal)
			optViews := make([]ui.View, 0, len(sp.Options))
			for _, opt := range sp.Options {
				o := opt
				lbl := o.Label
				if o.Value == curStr {
					lbl = "* " + lbl
				}
				optViews = append(optViews, ui.Button(lbl).OnClick(func() {
					s.applySetting(sp.Key, o.Value)
				}))
			}
			settingRows = append(settingRows, ui.HBox(
				ui.Label(meta).MainWeight(1),
				ui.ButtonGroup(optViews...),
			).Spacing(8))

		default:
			draft, ok := s.settingDraft[sp.Key]
			if !ok {
				ds := ui.MakeState(fmt.Sprint(curVal))
				s.settingDraft[sp.Key] = &ds
				draft = &ds
			}
			settingRows = append(settingRows, ui.HBox(
				ui.Label(meta).MainWeight(1),
				ui.TextInput().BindText(draft).MinWidth(140),
				ui.Button("Apply").OnClick(func() {
					raw := strings.TrimSpace(draft.Peek())
					var parsed any = raw
					if sp.Type == ckframework.SettingInt {
						n, err := strconv.Atoi(raw)
						if err != nil {
							s.statusMsg.Set(sp.Label + " must be a whole number")
							return
						}
						parsed = n
					} else if sp.Type == ckframework.SettingNumber {
						f, err := strconv.ParseFloat(raw, 64)
						if err != nil {
							s.statusMsg.Set(sp.Label + " must be a number")
							return
						}
						parsed = f
					}
					s.applySetting(sp.Key, parsed)
				}),
			).Spacing(8))
		}
	}

	actionRows := []ui.View{ui.Label("ADMIN ACTIONS / TRIGGERS")}
	for _, act := range schema.Actions {
		a := act
		actionRows = append(actionRows, ui.HBox(
			ui.Label(fmt.Sprintf("%s  [%s] — %s", a.Label, a.Target, nonEmpty(a.Help, a.ID))).MainWeight(1),
			ui.Button("Trigger").OnClick(func() {
				res, err := s.srv.Hub().TriggerAction(context.Background(), a.ID, nil, nil)
				if err != nil {
					s.statusMsg.Set("Action failed: " + err.Error())
				} else {
					s.statusMsg.Set(fmt.Sprintf("%s: %s", a.Label, nonEmpty(res.Message, "OK")))
				}
				s.tick.Update(func(x int) int { return x + 1 })
			}),
		).Spacing(8))
	}

	return ui.ScrollView(
		ui.VBox(
			ui.BorderBox(ui.VBox(settingRows...).Spacing(6).Padding(12)),
			ui.BorderBox(ui.VBox(actionRows...).Spacing(6).Padding(12)),
		).Spacing(12).Padding(4),
	).MainWeight(1)
}

func (s *launcherUIState) applySetting(key string, value any) {
	if _, _, _, _, err := s.srv.Hub().ApplyConfigPatch(context.Background(), -1, map[string]any{key: value}, "launcher"); err != nil {
		s.statusMsg.Set("Setting error: " + err.Error())
	} else {
		s.statusMsg.Set(fmt.Sprintf("Updated %s = %v (mirrored live)", key, value))
	}
	s.tick.Update(func(x int) int { return x + 1 })
}

func (s *launcherUIState) buildTelemetryTab(_ ui.App) ui.View {
	ctx := context.Background()
	role := s.telRole.Get()
	devID := s.telDeviceID.Get()
	cat := s.telCategory.Get()
	syncState := s.telSync.Get()
	search := s.telSearch.Get()

	devices, _ := s.srv.Store().ListDevices(ctx, false)
	events, _ := s.srv.Outbox().Query(ctx, cktelemetry.QueryFilter{
		OriginRole:     role,
		OriginDeviceID: devID,
		Category:       cat,
		SyncState:      syncState,
		Search:         search,
		Limit:          100,
	})

	// Fleet summary bar showing server + paired clients with key fingerprints & hardware info.
	fleetItems := make([]ui.View, 0, len(devices))
	devFilterBtns := []ui.View{
		ui.Button(markActive("All Keys", devID == "")).OnClick(func() { s.telDeviceID.Set("") }),
	}
	for _, d := range devices {
		dev := d
		fleetItems = append(fleetItems, ui.BorderBox(
			ui.VBox(
				ui.Label(fmt.Sprintf("[%s] %s", strings.ToUpper(dev.Role), dev.DeviceName)),
				ui.Label(fmt.Sprintf("Key: %s · %s %s (%s %s)", dev.DeviceID, dev.Manufacturer, nonEmpty(dev.Model, dev.DeviceType), dev.OSName, dev.OSVersion)),
				ui.Label(fmt.Sprintf("App v%s · IP %s", nonEmpty(dev.AppVersion, "-"), nonEmpty(dev.LastIP, "-"))),
			).Spacing(2).Padding(8),
		))
		label := fmt.Sprintf("%s (%s)", dev.DeviceName, dev.DeviceID[:6])
		devFilterBtns = append(devFilterBtns, ui.Button(markActive(label, devID == dev.DeviceID)).OnClick(func() {
			s.telDeviceID.Set(dev.DeviceID)
		}))
	}

	roleBtns := []ui.View{}
	for _, r := range []string{"all", "server", "tablet", "game"} {
		rv := r
		roleBtns = append(roleBtns, ui.Button(markActive(strings.ToUpper(rv), role == rv)).OnClick(func() { s.telRole.Set(rv) }))
	}
	catBtns := []ui.View{}
	for _, c := range []string{"all", "wifi", "battery", "ws", "launcher", "form", "board", "audit", "system"} {
		cv := c
		catBtns = append(catBtns, ui.Button(markActive(cv, cat == cv)).OnClick(func() { s.telCategory.Set(cv) }))
	}
	syncBtns := []ui.View{}
	for _, sy := range []string{"all", "pending", "synced", "relayed"} {
		sv := sy
		syncBtns = append(syncBtns, ui.Button(markActive(sv, syncState == sv)).OnClick(func() { s.telSync.Set(sv) }))
	}

	rowViews := make([]ui.View, 0, len(events)+1)
	if len(events) == 0 {
		rowViews = append(rowViews, ui.Label("No telemetry events match the current filter."))
	}
	for _, ev := range events {
		syncTag := "PENDING"
		if ev.CloudSyncedAtMs != nil {
			syncTag = "SYNCED"
		}
		relayTag := ""
		if ev.RelayedFrom != "" {
			relayTag = " via " + ev.RelayedFrom
		}
		payloadRaw, _ := json.Marshal(ev.Payload)
		line := fmt.Sprintf("%s  [%s]  %s (key:%s %s)  %s/%s  [%s%s]  %s",
			time.UnixMilli(ev.OccurredAtMs).Format("15:04:05"),
			strings.ToUpper(ev.OriginRole),
			nonEmpty(ev.DeviceName, ev.OriginDeviceID),
			ev.OriginDeviceID,
			nonEmpty(ev.Model, ev.DeviceType),
			ev.Category,
			ev.Kind,
			syncTag,
			relayTag,
			string(payloadRaw),
		)
		rowViews = append(rowViews, ui.BorderBox(
			ui.VBox(ui.Label(line).Selectable(true)).Padding(6),
		))
	}

	return ui.VBox(
		ui.HBox(fleetItems...).Spacing(8),
		ui.HBox(ui.Label("Origin:"), ui.ButtonGroup(roleBtns...), ui.Label("Sync:"), ui.ButtonGroup(syncBtns...)).Spacing(8),
		ui.HBox(ui.Label("Device/Key:"), ui.ButtonGroup(devFilterBtns...)).Spacing(8),
		ui.HBox(ui.Label("Category:"), ui.ButtonGroup(catBtns...)).Spacing(8),
		ui.HBox(
			ui.TextInput().BindText(&s.telSearch).Placeholder("Search kind, payload, key, device…").MainWeight(1),
			ui.Button("Refresh").OnClick(func() { s.tick.Update(func(x int) int { return x + 1 }) }),
		).Spacing(8),
		ui.ScrollView(ui.VBox(rowViews...).Spacing(4).Padding(4)).MainWeight(1),
	).Spacing(8).MainWeight(1)
}

func (s *launcherUIState) buildFormsTab(_ ui.App) ui.View {
	ctx := context.Background()
	form, ok := s.srv.Spec().DefaultForm()
	if !ok {
		return ui.Label("No forms declared in AppSpec.")
	}
	search := s.formSearch.Get()
	items, total, _ := s.srv.Store().ListSubmissions(ctx, form.ID, search, 150, 0)

	rows := make([]ui.View, 0, len(items)+1)
	rows = append(rows, ui.Label(fmt.Sprintf("Form: %s (%d total immutable rows · CKTelemetryTransmit=%v)", form.Title, total, form.TelemetryTransmit)))
	for _, it := range items {
		parts := make([]string, 0, len(form.Fields))
		for _, f := range form.Fields {
			parts = append(parts, fmt.Sprintf("%s=%v", f.Label, it.Payload[f.Key]))
		}
		outcomeStr := ""
		if len(it.Outcome) > 0 {
			raw, _ := json.Marshal(it.Outcome)
			outcomeStr = " · outcome=" + string(raw)
		}
		line := fmt.Sprintf("%s  |  %s (key:%s)  |  [%s]  |  %s%s",
			time.UnixMilli(it.CreatedAtMs).Format("2006-01-02 15:04:05"),
			nonEmpty(it.SourceDevice, it.SourceDeviceID),
			it.SourceDeviceID,
			it.Status,
			strings.Join(parts, " · "),
			outcomeStr,
		)
		rows = append(rows, ui.BorderBox(ui.VBox(ui.Label(line).Selectable(true)).Padding(6)))
	}

	return ui.VBox(
		ui.HBox(
			ui.TextInput().BindText(&s.formSearch).Placeholder("Search submissions…").MainWeight(1),
			ui.Button("Export CSV").OnClick(func() {
				csvData, err := s.srv.Store().ExportSubmissionsCSV(context.Background(), form)
				if err != nil {
					s.statusMsg.Set("Export failed: " + err.Error())
					return
				}
				outPath := filepath.Join("data", fmt.Sprintf("%s-%s-%s.csv", s.srv.Spec().Name, form.ID, time.Now().Format("20060102-150405")))
				_ = os.MkdirAll("data", 0o755)
				if err := os.WriteFile(outPath, []byte(csvData), 0o644); err != nil {
					s.statusMsg.Set("Write CSV failed: " + err.Error())
					return
				}
				s.statusMsg.Set("Exported CSV to " + outPath)
			}),
		).Spacing(8),
		ui.ScrollView(ui.VBox(rows...).Spacing(4).Padding(4)).MainWeight(1),
	).Spacing(8).MainWeight(1)
}

func (s *launcherUIState) buildBoardsTab(_ ui.App) ui.View {
	snap := s.srv.Hub().Snapshot()
	sections := make([]ui.View, 0, len(snap.Boards))
	for _, b := range snap.Boards {
		board := b
		rows := []ui.View{ui.Label(fmt.Sprintf("%s (%d active rows — Delete removes from display only; form history stays intact)", board.Title, len(board.Rows)))}
		for _, r := range board.Rows {
			row := r
			dispRaw, _ := json.Marshal(row.Display)
			rows = append(rows, ui.HBox(
				ui.Label(fmt.Sprintf("#%d  score=%.0f  %s", row.Rank, row.SortValue, string(dispRaw))).Selectable(true).MainWeight(1),
				ui.Button("Delete Row").OnClick(func() {
					if err := s.srv.Hub().DeleteBoardRow(context.Background(), board.BoardID, row.EntryID, "launcher"); err != nil {
						s.statusMsg.Set("Delete failed: " + err.Error())
					} else {
						s.statusMsg.Set("Removed row " + row.EntryID + " from " + board.Title)
					}
					s.tick.Update(func(x int) int { return x + 1 })
				}),
			).Spacing(8))
		}
		sections = append(sections, ui.BorderBox(ui.VBox(rows...).Spacing(6).Padding(10)))
	}
	if len(sections) == 0 {
		sections = append(sections, ui.Label("No boards configured."))
	}
	return ui.ScrollView(ui.VBox(sections...).Spacing(10).Padding(4)).MainWeight(1)
}

func (s *launcherUIState) buildDevicesTab(_ ui.App) ui.View {
	devices, _ := s.srv.Store().ListDevices(context.Background(), false)
	rows := make([]ui.View, 0, len(devices))
	for _, d := range devices {
		dev := d
		summary := fmt.Sprintf("[%s] %s  |  Key: %s  |  %s %s (%s %s)  |  app v%s · fw v%s · p%d  |  IP %s",
			strings.ToUpper(dev.Role),
			dev.DeviceName,
			dev.DeviceID,
			dev.Manufacturer,
			nonEmpty(dev.Model, dev.DeviceType),
			dev.OSName,
			dev.OSVersion,
			nonEmpty(dev.AppVersion, "-"),
			nonEmpty(dev.FrameworkVersion, "-"),
			dev.ProtocolVersion,
			nonEmpty(dev.LastIP, "-"),
		)
		var right ui.View = ui.Label("Local Server")
		if dev.Role != "server" {
			right = ui.Button("Revoke Key").OnClick(func() {
				if err := s.srv.Store().RevokeDevice(context.Background(), dev.DeviceID, time.Now().UnixMilli()); err != nil {
					s.statusMsg.Set("Revoke failed: " + err.Error())
				} else {
					s.statusMsg.Set("Revoked device key " + dev.DeviceID)
				}
				s.tick.Update(func(x int) int { return x + 1 })
			})
		}
		rows = append(rows, ui.BorderBox(
			ui.HBox(ui.Label(summary).Selectable(true).MainWeight(1), right).Spacing(8).Padding(8),
		))
	}
	return ui.ScrollView(ui.VBox(rows...).Spacing(6).Padding(4)).MainWeight(1)
}

func markActive(label string, active bool) string {
	if active {
		return "[" + label + "]"
	}
	return label
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func sanitizeID(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, strings.ToLower(s))
}
