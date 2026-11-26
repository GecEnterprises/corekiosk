package cklauncher

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/ckserver"
	"github.com/GecEnterprises/corekiosk/go/cktelemetry"
)

// RunNativeUI launches the Fyne v2 desktop console for cklauncher.
func RunNativeUI(ctx context.Context, srv *ckserver.Server, cd *Countdown, sup *Supervisor, port string) error {
	appID := "org.gecenterprises.corekiosk." + sanitizeID(srv.Spec().Name)
	a := app.NewWithID(appID)
	a.Settings().SetTheme(newKioskTheme())

	w := a.NewWindow(srv.Spec().Name + " - corekiosk")
	w.Resize(fyne.NewSize(1180, 760))
	w.CenterOnScreen()

	statusLabel := widget.NewLabel("Ready")
	versionFooter := widget.NewLabel("")
	versionFooter.TextStyle = fyne.TextStyle{Monospace: true}
	var uiStarted atomic.Bool
	a.Lifecycle().SetOnStarted(func() { uiStarted.Store(true) })
	setStatus := func(msg string) {
		if uiStarted.Load() {
			fyne.Do(func() { statusLabel.SetText(msg) })
		} else {
			statusLabel.SetText(msg)
		}
	}

	// --- Top Bar & Launch Strip ---
	titleText := canvas.NewText(srv.Spec().Name, color.NRGBA{R: 236, G: 241, B: 248, A: 255})
	titleText.TextStyle = fyne.TextStyle{Bold: true}
	titleText.TextSize = 17

	wifiPill := widget.NewLabel("Wi-Fi: -")
	initialAddrs := ckserver.LocalAddresses(port)
	lanPill := widget.NewLabel(initialAddrs[0])
	lanPill.TextStyle = fyne.TextStyle{Monospace: true}
	pairPill := widget.NewLabel("")
	pairPill.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	bridgePill := widget.NewLabel("")
	cloudPill := widget.NewLabel("")

	companionTopBtn := widget.NewButtonWithIcon("Companion", theme.ComputerIcon(), func() {
		compSt := srv.CompanionStatus()
		if !compSt.Listening || compSt.Port == "" {
			setStatus("Companion web not bundled")
			return
		}
		target := "http://127.0.0.1:" + compSt.Port
		if err := OpenBrowser(target); err != nil {
			setStatus("Open failed: " + err.Error())
		} else {
			setStatus("Opened " + target)
		}
	})
	if !srv.CompanionStatus().Listening {
		companionTopBtn.Disable()
	}

	syncBtn := widget.NewButtonWithIcon("Sync", theme.UploadIcon(), func() {
		setStatus("Syncing…")
		go func() {
			n, err := srv.Shipper().FlushNow(context.Background())
			if err != nil {
				setStatus("Sync failed: " + err.Error())
			} else {
				setStatus(fmt.Sprintf("Synced %d", n))
			}
		}()
	})

	countdownLabel := widget.NewLabel("")
	countdownLabel.TextStyle = fyne.TextStyle{Bold: true}
	countdownBar := widget.NewProgressBar()
	countdownBar.TextFormatter = func() string { return "" }

	pauseBtn := widget.NewButtonWithIcon("Pause", theme.MediaPauseIcon(), func() {
		if cd.State().Paused {
			cd.Resume()
		} else {
			cd.Pause()
		}
	})
	launchBtn := widget.NewButtonWithIcon("Launch", theme.MediaPlayIcon(), func() {
		cd.LaunchNow()
		setStatus("Launching…")
	})
	launchBtn.Importance = widget.HighImportance
	restartBtn := widget.NewButtonWithIcon("Restart", theme.ViewRefreshIcon(), func() {
		cd.LaunchNow()
		sup.Restart()
		setStatus("Restarted")
	})
	stopBtn := widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		sup.Stop()
		setStatus("Stopped")
	})
	stopBtn.Importance = widget.DangerImportance

	topBar := container.NewVBox(
		container.NewPadded(container.NewHBox(
			titleText,
			layout.NewSpacer(),
			statusChip(wifiPill),
			statusChip(lanPill),
			statusChip(pairPill),
			statusChip(bridgePill),
			statusChip(cloudPill),
			companionTopBtn,
			syncBtn,
		)),
		widget.NewSeparator(),
		container.NewPadded(container.NewBorder(
			nil, nil,
			countdownLabel,
			container.NewHBox(pauseBtn, launchBtn, restartBtn, stopBtn),
			countdownBar,
		)),
		widget.NewSeparator(),
	)

	// --- Tab 1: Booth (2-column compact layout, zero filler text) ---
	ovWifi := widget.NewLabel("-")
	ovWifi.TextStyle = fyne.TextStyle{Bold: true}
	ovListener := widget.NewLabel("-")
	ovListener.TextStyle = fyne.TextStyle{Monospace: true}
	relistenBtn := widget.NewButtonWithIcon("Relisten", theme.ViewRefreshIcon(), func() {
		setStatus("Rebinding listener…")
		go func() {
			if err := srv.RestartListener("launcher_ui"); err != nil {
				setStatus("Relisten failed: " + err.Error())
				return
			}
			st := srv.ListenerStatus()
			setStatus(fmt.Sprintf("Listening on %s (gen %d)", st.BoundAddr, st.Generation))
		}()
	})

	ovAddrsBox := container.NewVBox()
	lastAddrSig := ""
	renderAddresses := func(addrs []string) {
		sig := strings.Join(addrs, ",")
		if sig == lastAddrSig {
			return
		}
		lastAddrSig = sig
		rows := make([]fyne.CanvasObject, 0, len(addrs))
		for _, addr := range addrs {
			a := addr
			lbl := widget.NewLabel(a)
			lbl.TextStyle = fyne.TextStyle{Monospace: true}
			btn := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
				w.Clipboard().SetContent(a)
				setStatus("Copied " + a)
			})
			rows = append(rows, container.NewHBox(lbl, btn))
		}
		ovAddrsBox.Objects = rows
		ovAddrsBox.Refresh()
	}
	renderAddresses(initialAddrs)

	ovCompanion := widget.NewLabel("-")
	ovCompanion.TextStyle = fyne.TextStyle{Monospace: true}
	copyCompBtn := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
		compSt := srv.CompanionStatus()
		if len(compSt.Addresses) > 0 {
			u := "http://" + compSt.Addresses[0]
			w.Clipboard().SetContent(u)
			setStatus("Copied " + u)
		}
	})
	openCompBtn := widget.NewButtonWithIcon("Open", theme.ComputerIcon(), func() {
		compSt := srv.CompanionStatus()
		if compSt.Listening && compSt.Port != "" {
			u := "http://127.0.0.1:" + compSt.Port
			if err := OpenBrowser(u); err != nil {
				setStatus("Open failed: " + err.Error())
			} else {
				setStatus("Opened " + u)
			}
		}
	})

	ovServerID := widget.NewLabel(srv.Identity().ServerID)
	ovServerID.TextStyle = fyne.TextStyle{Monospace: true}
	ovPairCode := widget.NewLabel(srv.DeviceAuth().PairCode())
	ovPairCode.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}

	rotateBtn := widget.NewButtonWithIcon("Rotate", theme.ViewRefreshIcon(), func() {
		code := srv.DeviceAuth().RotatePairCode()
		ovPairCode.SetText(code)
		setStatus("Pair code: " + code)
	})

	ovPending := widget.NewLabel("0")
	ovSyncErr := widget.NewLabel("-")

	pairingPanel := sectionPanel("Pairing & Network", container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Wi-Fi", ovWifi),
			widget.NewFormItem("Listener", container.NewHBox(ovListener, relistenBtn)),
			widget.NewFormItem("Addresses", ovAddrsBox),
			widget.NewFormItem("Companion Web", container.NewHBox(ovCompanion, copyCompBtn, openCompBtn)),
			widget.NewFormItem("Server Key", ovServerID),
			widget.NewFormItem("Pair Code", container.NewHBox(ovPairCode, rotateBtn)),
		),
		widget.NewSeparator(),
		widget.NewForm(
			widget.NewFormItem("Pending Sync", ovPending),
			widget.NewFormItem("Sync Error", ovSyncErr),
		),
	))

	ovOpUser := widget.NewLabel("")
	ovOpUser.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
	ovOpPass := widget.NewLabel("")
	ovOpPass.TextStyle = fyne.TextStyle{Monospace: true}

	newOpUser := widget.NewEntry()
	newOpUser.SetPlaceHolder("Username")
	newOpPass := widget.NewPasswordEntry()
	newOpPass.SetPlaceHolder("Password")

	saveOpBtn := widget.NewButtonWithIcon("Update", theme.ConfirmIcon(), func() {
		ops, err := srv.Store().ListOperators(context.Background())
		if err != nil || len(ops) == 0 {
			setStatus("No operator account")
			return
		}
		u := strings.TrimSpace(newOpUser.Text)
		p := newOpPass.Text
		if u == "" && p == "" {
			return
		}
		if err := srv.OperatorAuth().UpdateCredentials(context.Background(), ops[0].ID, u, p); err != nil {
			dialog.ShowError(err, w)
			return
		}
		newOpUser.SetText("")
		newOpPass.SetText("")
		setStatus("Operator updated")
	})
	saveOpBtn.Importance = widget.HighImportance

	operatorPanel := sectionPanel("Operator", container.NewVBox(
		widget.NewForm(
			widget.NewFormItem("Username", ovOpUser),
			widget.NewFormItem("Password", ovOpPass),
		),
		widget.NewSeparator(),
		widget.NewForm(
			widget.NewFormItem("New User", newOpUser),
			widget.NewFormItem("New Pass", newOpPass),
		),
		container.NewHBox(layout.NewSpacer(), saveOpBtn),
	))

	boothTab := container.NewVScroll(container.NewPadded(
		container.NewGridWithColumns(2, pairingPanel, operatorPanel),
	))

	// --- Tab 2: Controls (Left: Settings grouped cleanly, Right: Actions) ---
	settingsCol := container.NewVBox()
	actionsCol := container.NewVBox()

	rebuildControls := func() {
		_, vals, rtVals, settings := srv.Hub().ConfigState()
		schema := srv.Hub().Schema()

		// Group settings by SettingSpec.Group preserving order.
		groupOrder := []string{}
		byGroup := map[string][]ckframework.SettingSpec{}
		for _, sp := range settings {
			g := nonEmpty(sp.Group, "General")
			if _, exists := byGroup[g]; !exists {
				groupOrder = append(groupOrder, g)
			}
			byGroup[g] = append(byGroup[g], sp)
		}

		groupPanels := make([]fyne.CanvasObject, 0, len(groupOrder))
		for _, g := range groupOrder {
			rows := make([]fyne.CanvasObject, 0, len(byGroup[g]))
			for _, spec := range byGroup[g] {
				sp := spec
				var curVal any
				if sp.Scope == ckframework.ScopeRuntime {
					curVal = rtVals[sp.Key]
				} else {
					curVal = vals[sp.Key]
				}
				applyVal := func(v any) {
					if _, _, _, _, err := srv.Hub().ApplyConfigPatch(context.Background(), -1, map[string]any{sp.Key: v}, "launcher"); err != nil {
						setStatus(err.Error())
					} else {
						setStatus(fmt.Sprintf("%s = %v", sp.Label, v))
					}
				}

				tag := settingTag(sp)
				labelText := sp.Label
				if tag != "" {
					labelText += "  (" + tag + ")"
				}
				lbl := widget.NewLabel(labelText)

				switch sp.Type {
				case ckframework.SettingBool:
					b, _ := curVal.(bool)
					chk := widget.NewCheck("", nil)
					chk.SetChecked(b)
					chk.OnChanged = func(checked bool) { applyVal(checked) }
					rows = append(rows, container.NewBorder(nil, nil, lbl, chk))

				case ckframework.SettingEnum:
					labels := make([]string, 0, len(sp.Options))
					valByLabel := make(map[string]string, len(sp.Options))
					selectedLabel := ""
					curStr := fmt.Sprint(curVal)
					for _, opt := range sp.Options {
						labels = append(labels, opt.Label)
						valByLabel[opt.Label] = opt.Value
						if opt.Value == curStr {
							selectedLabel = opt.Label
						}
					}
					sel := widget.NewSelect(labels, nil)
					sel.Selected = selectedLabel
					sel.OnChanged = func(chosen string) {
						if v, ok := valByLabel[chosen]; ok {
							applyVal(v)
						}
					}
					rows = append(rows, container.NewBorder(nil, nil, lbl, sel))

				case ckframework.SettingInt, ckframework.SettingNumber:
					if sp.Min != nil && sp.Max != nil {
						curF, _ := toFloat64(curVal)
						valLbl := widget.NewLabel(formatUnit(curF, sp.Unit))
						valLbl.TextStyle = fyne.TextStyle{Monospace: true}
						sl := widget.NewSlider(*sp.Min, *sp.Max)
						if sp.Step != nil && *sp.Step > 0 {
							sl.Step = *sp.Step
						}
						sl.Value = curF
						sl.OnChanged = func(v float64) { valLbl.SetText(formatUnit(v, sp.Unit)) }
						sl.OnChangeEnded = func(v float64) {
							if sp.Type == ckframework.SettingInt {
								applyVal(int(v))
							} else {
								applyVal(v)
							}
						}
						rows = append(rows, container.NewBorder(nil, nil, lbl, valLbl, sl))
					} else {
						ent := widget.NewEntry()
						ent.SetText(fmt.Sprint(curVal))
						ent.OnSubmitted = func(s string) {
							if sp.Type == ckframework.SettingInt {
								if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
									applyVal(n)
								}
							} else if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
								applyVal(f)
							}
						}
						rows = append(rows, container.NewBorder(nil, nil, lbl, nil, ent))
					}

				default:
					ent := widget.NewEntry()
					ent.SetText(fmt.Sprint(curVal))
					ent.OnSubmitted = func(s string) { applyVal(s) }
					rows = append(rows, container.NewBorder(nil, nil, lbl, nil, ent))
				}
			}
			groupPanels = append(groupPanels, sectionPanel(g, container.NewVBox(rows...)))
		}
		settingsCol.Objects = groupPanels
		settingsCol.Refresh()

		actBtns := make([]fyne.CanvasObject, 0, len(schema.Actions))
		for _, act := range schema.Actions {
			a := act
			fire := func(params map[string]any) {
				go func() {
					res, err := srv.Hub().TriggerAction(context.Background(), a.ID, params, nil)
					if err != nil {
						setStatus(err.Error())
					} else {
						setStatus(nonEmpty(res.Message, a.Label+" OK"))
					}
				}()
			}
			btn := widget.NewButton(a.Label, func() {
				if len(a.Params) > 0 {
					formItems := make([]*widget.FormItem, 0, len(a.Params))
					entries := make(map[string]*widget.Entry, len(a.Params))
					for _, pf := range a.Params {
						e := widget.NewEntry()
						e.SetPlaceHolder(pf.Placeholder)
						entries[pf.Key] = e
						formItems = append(formItems, widget.NewFormItem(pf.Label, e))
					}
					dialog.ShowForm(a.Label, "Run", "Cancel", formItems, func(ok bool) {
						if !ok {
							return
						}
						p := make(map[string]any, len(entries))
						for k, e := range entries {
							p[k] = e.Text
						}
						fire(p)
					}, w)
					return
				}
				if a.Confirm != ckframework.ConfirmNone {
					dialog.ShowConfirm(a.Label, nonEmpty(a.ConfirmText, a.Label+"?"), func(ok bool) {
						if ok {
							fire(nil)
						}
					}, w)
					return
				}
				fire(nil)
			})
			if a.Style == ckframework.ActionStyleDanger {
				btn.Importance = widget.DangerImportance
			} else if a.Style == ckframework.ActionStylePrimary {
				btn.Importance = widget.HighImportance
			}
			actBtns = append(actBtns, btn)
		}
		actionsCol.Objects = []fyne.CanvasObject{sectionPanel("Actions", container.NewVBox(actBtns...))}
		actionsCol.Refresh()
	}

	controlsSplit := container.NewHSplit(
		container.NewVScroll(container.NewPadded(settingsCol)),
		container.NewVScroll(container.NewPadded(actionsCol)),
	)
	controlsSplit.Offset = 0.72

	// --- Tab 3: Telemetry Explorer ---
	var telEvents []cktelemetry.EventWithDevice
	deviceMapByLabel := map[string]string{"All devices": ""}
	roleFilter := widget.NewSelect([]string{"all", "server", "tablet", "game"}, nil)
	roleFilter.Selected = "all"
	devFilter := widget.NewSelect([]string{"All devices"}, nil)
	devFilter.Selected = "All devices"
	catFilter := widget.NewSelect([]string{"all", "wifi", "battery", "ws", "launcher", "form", "board", "audit", "system"}, nil)
	catFilter.Selected = "all"
	syncFilter := widget.NewSelect([]string{"all", "pending", "synced", "relayed"}, nil)
	syncFilter.Selected = "all"
	telSearch := widget.NewEntry()
	telSearch.SetPlaceHolder("Search…")

	telHeaders := []string{"Time", "Origin", "Device / Key", "Event", "Sync", "Payload"}
	telTable := widget.NewTable(
		func() (int, int) { return len(telEvents), len(telHeaders) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.TableCellID, obj fyne.CanvasObject) {
			l := obj.(*widget.Label)
			ev := telEvents[id.Row]
			switch id.Col {
			case 0:
				l.TextStyle = fyne.TextStyle{Monospace: true}
				l.SetText(time.UnixMilli(ev.OccurredAtMs).Format("15:04:05"))
			case 1:
				l.TextStyle = fyne.TextStyle{Bold: true}
				l.SetText(strings.ToUpper(ev.OriginRole))
			case 2:
				l.TextStyle = fyne.TextStyle{}
				devLabel := nonEmpty(ev.DeviceName, ev.OriginDeviceID)
				if ev.Model != "" {
					l.SetText(fmt.Sprintf("%s / %s (%s)", devLabel, ev.Model, ev.OriginDeviceID))
				} else {
					l.SetText(fmt.Sprintf("%s (%s)", devLabel, ev.OriginDeviceID))
				}
			case 3:
				l.TextStyle = fyne.TextStyle{Monospace: true}
				l.SetText(ev.Kind)
			case 4:
				l.TextStyle = fyne.TextStyle{}
				s := "pending"
				if ev.CloudSyncedAtMs != nil {
					s = "synced"
				}
				if ev.RelayedFrom != "" {
					s += " ← " + ev.RelayedFrom[:6]
				}
				l.SetText(s)
			case 5:
				l.TextStyle = fyne.TextStyle{Monospace: true}
				raw, _ := json.Marshal(ev.Payload)
				l.SetText(string(raw))
			}
		},
	)
	telTable.ShowHeaderRow = true
	telTable.CreateHeader = func() fyne.CanvasObject {
		return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	telTable.UpdateHeader = func(id widget.TableCellID, template fyne.CanvasObject) {
		if id.Row < 0 && id.Col >= 0 && id.Col < len(telHeaders) {
			template.(*widget.Label).SetText(telHeaders[id.Col])
		}
	}
	telTable.SetColumnWidth(0, 76)
	telTable.SetColumnWidth(1, 74)
	telTable.SetColumnWidth(2, 230)
	telTable.SetColumnWidth(3, 180)
	telTable.SetColumnWidth(4, 110)
	telTable.SetColumnWidth(5, 360)
	telTable.OnSelected = func(id widget.TableCellID) {
		if id.Row < 0 || id.Row >= len(telEvents) {
			return
		}
		ev := telEvents[id.Row]
		pretty, _ := json.MarshalIndent(ev, "", "  ")
		txt := widget.NewMultiLineEntry()
		txt.SetText(string(pretty))
		txt.TextStyle = fyne.TextStyle{Monospace: true}
		sc := container.NewVScroll(txt)
		sc.SetMinSize(fyne.NewSize(600, 380))
		dialog.ShowCustom(ev.Kind+" ("+ev.EventID+")", "Close", sc, w)
		telTable.UnselectAll()
	}

	reloadTelemetry := func() {
		ctxBG := context.Background()
		devs, _ := srv.Store().ListDevices(ctxBG, false)
		opts := []string{"All devices"}
		deviceMapByLabel = map[string]string{"All devices": ""}
		for _, d := range devs {
			lbl := fmt.Sprintf("%s (%s)", d.DeviceName, d.DeviceID)
			opts = append(opts, lbl)
			deviceMapByLabel[lbl] = d.DeviceID
		}
		devFilter.Options = opts

		items, err := srv.Outbox().Query(ctxBG, cktelemetry.QueryFilter{
			OriginRole:     roleFilter.Selected,
			OriginDeviceID: deviceMapByLabel[devFilter.Selected],
			Category:       catFilter.Selected,
			SyncState:      syncFilter.Selected,
			Search:         telSearch.Text,
			Limit:          200,
		})
		if err == nil {
			telEvents = items
			telTable.Refresh()
		}
	}
	roleFilter.OnChanged = func(string) { reloadTelemetry() }
	devFilter.OnChanged = func(string) { reloadTelemetry() }
	catFilter.OnChanged = func(string) { reloadTelemetry() }
	syncFilter.OnChanged = func(string) { reloadTelemetry() }
	telSearch.OnChanged = func(string) { reloadTelemetry() }

	telemetryTab := container.NewBorder(
		container.NewPadded(container.NewBorder(
			nil, nil,
			container.NewHBox(roleFilter, devFilter, catFilter, syncFilter),
			widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), reloadTelemetry),
			telSearch,
		)),
		nil, nil, nil,
		telTable,
	)

	// --- Tab 4: Forms Table ---
	formSpec, hasForm := srv.Spec().DefaultForm()
	var subItems []ckframework.SubmissionRecord
	formSearch := widget.NewEntry()
	formSearch.SetPlaceHolder("Search…")

	formCols := []string{"Time", "Client", "Status"}
	for _, f := range formSpec.Fields {
		formCols = append(formCols, f.Label)
	}
	formCols = append(formCols, "Outcome")

	formsTable := widget.NewTable(
		func() (int, int) { return len(subItems), len(formCols) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.TableCellID, obj fyne.CanvasObject) {
			l := obj.(*widget.Label)
			it := subItems[id.Row]
			switch {
			case id.Col == 0:
				l.TextStyle = fyne.TextStyle{Monospace: true}
				l.SetText(time.UnixMilli(it.CreatedAtMs).Format("01-02 15:04:05"))
			case id.Col == 1:
				l.TextStyle = fyne.TextStyle{}
				l.SetText(fmt.Sprintf("%s (%s)", nonEmpty(it.SourceDevice, it.SourceDeviceID), it.SourceDeviceID[:min(6, len(it.SourceDeviceID))]))
			case id.Col == 2:
				l.TextStyle = fyne.TextStyle{}
				l.SetText(it.Status)
			case id.Col == len(formCols)-1:
				l.TextStyle = fyne.TextStyle{Monospace: true}
				if len(it.Outcome) > 0 {
					raw, _ := json.Marshal(it.Outcome)
					l.SetText(string(raw))
				} else {
					l.SetText("-")
				}
			default:
				l.TextStyle = fyne.TextStyle{}
				idx := id.Col - 3
				if idx >= 0 && idx < len(formSpec.Fields) {
					v := it.Payload[formSpec.Fields[idx].Key]
					if v != nil {
						l.SetText(fmt.Sprint(v))
					} else {
						l.SetText("")
					}
				}
			}
		},
	)
	formsTable.ShowHeaderRow = true
	formsTable.CreateHeader = func() fyne.CanvasObject {
		return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	formsTable.UpdateHeader = func(id widget.TableCellID, template fyne.CanvasObject) {
		if id.Row < 0 && id.Col >= 0 && id.Col < len(formCols) {
			template.(*widget.Label).SetText(formCols[id.Col])
		}
	}
	for i := range formCols {
		formsTable.SetColumnWidth(i, 150)
	}

	reloadForms := func() {
		if !hasForm {
			return
		}
		items, _, err := srv.Store().ListSubmissions(context.Background(), formSpec.ID, formSearch.Text, 300, 0)
		if err == nil {
			subItems = items
			formsTable.Refresh()
		}
	}
	formSearch.OnChanged = func(string) { reloadForms() }

	exportBtn := widget.NewButtonWithIcon("Export CSV", theme.DownloadIcon(), func() {
		if !hasForm {
			return
		}
		csvData, err := srv.Store().ExportSubmissionsCSV(context.Background(), formSpec)
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		_ = os.MkdirAll("data", 0o755)
		outPath := filepath.Join("data", fmt.Sprintf("%s-%s-%s.csv", srv.Spec().Name, formSpec.ID, time.Now().Format("20060102-150405")))
		if err := os.WriteFile(outPath, []byte(csvData), 0o644); err != nil {
			dialog.ShowError(err, w)
			return
		}
		setStatus("Exported " + outPath)
	})
	exportBtn.Importance = widget.HighImportance

	formsTab := container.NewBorder(
		container.NewPadded(container.NewBorder(
			nil, nil, nil,
			container.NewHBox(widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), reloadForms), exportBtn),
			formSearch,
		)),
		nil, nil, nil,
		formsTable,
	)

	// --- Tab 5: Boards ---
	boardsBox := container.NewVBox()
	var reloadBoards func()
	reloadBoards = func() {
		snap := srv.Hub().Snapshot()
		panels := []fyne.CanvasObject{}
		for _, b := range snap.Boards {
			board := b
			rows := []fyne.CanvasObject{}
			for _, r := range board.Rows {
				row := r
				dispRaw, _ := json.Marshal(row.Display)
				delBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
					dialog.ShowConfirm("Delete Row", "Remove #"+strconv.Itoa(row.Rank)+" from "+board.Title+"?", func(ok bool) {
						if !ok {
							return
						}
						if err := srv.Hub().DeleteBoardRow(context.Background(), board.BoardID, row.EntryID, "launcher"); err != nil {
							dialog.ShowError(err, w)
							return
						}
						setStatus("Removed row")
						reloadBoards()
					}, w)
				})
				delBtn.Importance = widget.DangerImportance
				rankLbl := widget.NewLabel(fmt.Sprintf("#%d  %.0f", row.Rank, row.SortValue))
				rankLbl.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}
				rows = append(rows, container.NewBorder(nil, nil, rankLbl, delBtn, widget.NewLabel(string(dispRaw))))
			}
			if len(rows) == 0 {
				rows = append(rows, widget.NewLabel("Empty"))
			}
			panels = append(panels, sectionPanel(fmt.Sprintf("%s (%d)", board.Title, len(board.Rows)), container.NewVBox(rows...)))
		}
		if len(panels) == 0 {
			panels = append(panels, widget.NewLabel("No boards"))
		}
		boardsBox.Objects = panels
		boardsBox.Refresh()
	}
	boardsTab := container.NewBorder(
		container.NewPadded(container.NewHBox(layout.NewSpacer(), widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), reloadBoards))),
		nil, nil, nil,
		container.NewVScroll(container.NewPadded(boardsBox)),
	)

	// --- Tab 6: Devices ---
	devicesBox := container.NewVBox()
	var reloadDevices func()
	reloadDevices = func() {
		devs, _ := srv.Store().ListDevices(context.Background(), false)
		rows := make([]fyne.CanvasObject, 0, len(devs))
		for _, d := range devs {
			dev := d
			left := widget.NewLabel(fmt.Sprintf("[%s]  %s", strings.ToUpper(dev.Role), dev.DeviceName))
			left.TextStyle = fyne.TextStyle{Bold: true}
			mid := widget.NewLabel(fmt.Sprintf("key=%s   %s %s (%s %s)   v%s   ip=%s",
				dev.DeviceID, dev.Manufacturer, nonEmpty(dev.Model, dev.DeviceType),
				dev.OSName, dev.OSVersion, nonEmpty(dev.AppVersion, "-"), nonEmpty(dev.LastIP, "-")))
			mid.TextStyle = fyne.TextStyle{Monospace: true}
			var right fyne.CanvasObject = widget.NewLabel("")
			if dev.Role != "server" {
				revBtn := widget.NewButtonWithIcon("Revoke", theme.DeleteIcon(), func() {
					dialog.ShowConfirm("Revoke Key", "Revoke "+dev.DeviceName+" ("+dev.DeviceID+")?", func(ok bool) {
						if !ok {
							return
						}
						if err := srv.Store().RevokeDevice(context.Background(), dev.DeviceID, time.Now().UnixMilli()); err != nil {
							dialog.ShowError(err, w)
							return
						}
						setStatus("Revoked " + dev.DeviceID)
						reloadDevices()
					}, w)
				})
				revBtn.Importance = widget.DangerImportance
				right = revBtn
			}
			rows = append(rows, container.NewBorder(nil, widget.NewSeparator(), left, right, mid))
		}
		devicesBox.Objects = rows
		devicesBox.Refresh()
	}
	devicesTab := container.NewBorder(
		container.NewPadded(container.NewHBox(layout.NewSpacer(), widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), reloadDevices))),
		nil, nil, nil,
		container.NewVScroll(container.NewPadded(devicesBox)),
	)

	// Left-rail navigation tabs (cleaner & easier to scan than cramped top tabs).
	tabs := container.NewAppTabs(
		container.NewTabItemWithIcon("Booth", theme.HomeIcon(), boothTab),
		container.NewTabItemWithIcon("Controls", theme.SettingsIcon(), controlsSplit),
		container.NewTabItemWithIcon("Telemetry", theme.ListIcon(), telemetryTab),
		container.NewTabItemWithIcon("Forms", theme.DocumentIcon(), formsTab),
		container.NewTabItemWithIcon("Boards", theme.GridIcon(), boardsTab),
		container.NewTabItemWithIcon("Devices", theme.ComputerIcon(), devicesTab),
	)
	tabs.SetTabLocation(container.TabLocationLeading)
	tabs.OnSelected = func(ti *container.TabItem) {
		switch ti.Text {
		case "Controls":
			rebuildControls()
		case "Telemetry":
			reloadTelemetry()
		case "Forms":
			reloadForms()
		case "Boards":
			reloadBoards()
		case "Devices":
			reloadDevices()
		}
	}

	computeLive := func() func() {
		v := srv.Hub().Versions()
		wifi := srv.Hub().WifiInfo()
		lnSt := srv.ListenerStatus()
		compSt := srv.CompanionStatus()
		addrs := lnSt.Addresses
		if len(addrs) == 0 {
			addrs = ckserver.LocalAddresses(port)
		}
		pending, _ := srv.Outbox().PendingCount(context.Background())
		hint := srv.OperatorAuth().BoothHint(context.Background())
		cdState := cd.State()
		supState := sup.Status()
		pairCode := srv.DeviceAuth().PairCode()
		syncErr := nonEmpty(srv.Shipper().LastError(), "-")
		displayOnline := srv.Hub().DisplayOnline()
		cloudOnline := srv.Shipper().CloudOnline()

		wifiShort := "Wi-Fi: Disconnected"
		wifiDetail := "Disconnected"
		if wifi.Connected && wifi.SSID != "" {
			wifiShort = "Wi-Fi: " + wifi.SSID
			if wifi.SignalPct > 0 {
				wifiDetail = fmt.Sprintf("%s (%d%%)", wifi.SSID, wifi.SignalPct)
			} else {
				wifiDetail = wifi.SSID
			}
		}

		lanText := addrs[0]
		if len(addrs) > 1 {
			lanText = fmt.Sprintf("%s (+%d)", addrs[0], len(addrs)-1)
		}
		listenerText := fmt.Sprintf("Active on %s (gen %d)", nonEmpty(lnSt.BoundAddr, ":"+port), lnSt.Generation)
		switch {
		case lnSt.Rebinding:
			lanText = "Rebinding :" + port
			listenerText = "Rebinding…"
		case !lnSt.Listening:
			lanText = "Listener down"
			if lnSt.LastError != "" {
				listenerText = "Error: " + lnSt.LastError
			} else {
				listenerText = "Stopped"
			}
		case !lnSt.LANOnline:
			lanText = fmt.Sprintf("LAN Offline (%s)", addrs[0])
			listenerText = fmt.Sprintf("Loopback only on %s (gen %d)", nonEmpty(lnSt.BoundAddr, ":"+port), lnSt.Generation)
		}

		bridgeStr := "Bridge: Offline"
		if displayOnline {
			bridgeStr = "Bridge: Online"
		}
		cloudStr := fmt.Sprintf("Cloud: Offline (%d)", pending)
		if cloudOnline {
			cloudStr = fmt.Sprintf("Cloud: Online (%d)", pending)
		}

		var headline string
		var progress float64
		switch {
		case cdState.Disabled:
			headline = "Server only"
			progress = 0
		case cdState.Launched && supState.Running:
			headline = fmt.Sprintf("Running (PID %d)", supState.PID)
			if supState.RestartCount > 0 {
				headline = fmt.Sprintf("Running (PID %d, restarts %d)", supState.PID, supState.RestartCount)
			}
			progress = 1
		case cdState.Launched:
			headline = fmt.Sprintf("Stopped (exit %d)", supState.LastExitCode)
			progress = 0
		case cdState.Paused:
			headline = fmt.Sprintf("Paused (%ds)", cdState.RemainingSeconds)
			if cdState.TotalSeconds > 0 {
				progress = 1 - float64(cdState.RemainingSeconds)/float64(cdState.TotalSeconds)
			}
		default:
			headline = fmt.Sprintf("Launching in %ds", cdState.RemainingSeconds)
			if cdState.TotalSeconds > 0 {
				progress = 1 - float64(cdState.RemainingSeconds)/float64(cdState.TotalSeconds)
			}
		}

		compText := "Not bundled"
		compActive := compSt.Listening && len(compSt.Addresses) > 0
		if compActive {
			compText = "http://" + compSt.Addresses[0]
		}

		return func() {
			wifiPill.SetText(wifiShort)
			ovWifi.SetText(wifiDetail)
			ovListener.SetText(listenerText)
			ovCompanion.SetText(compText)
			if compActive {
				companionTopBtn.Enable()
				copyCompBtn.Enable()
				openCompBtn.Enable()
			} else {
				companionTopBtn.Disable()
				copyCompBtn.Disable()
				openCompBtn.Disable()
			}
			lanPill.SetText(lanText)
			renderAddresses(addrs)
			pairPill.SetText("Pair " + pairCode)
			bridgePill.SetText(bridgeStr)
			cloudPill.SetText(cloudStr)
			countdownLabel.SetText(headline)
			countdownBar.SetValue(progress)
			if cdState.Paused {
				pauseBtn.SetText("Resume")
				pauseBtn.SetIcon(theme.MediaPlayIcon())
			} else {
				pauseBtn.SetText("Pause")
				pauseBtn.SetIcon(theme.MediaPauseIcon())
			}
			if cdState.Disabled || cdState.Launched {
				pauseBtn.Disable()
				launchBtn.Disable()
			} else {
				pauseBtn.Enable()
				launchBtn.Enable()
			}
			if supState.Running {
				stopBtn.Enable()
			} else {
				stopBtn.Disable()
			}

			ovPairCode.SetText(pairCode)
			ovOpUser.SetText(hint.Username)
			if hint.IsDefault {
				ovOpPass.SetText(hint.DefaultPassword + " (default)")
			} else {
				ovOpPass.SetText("••••••••")
			}
			ovPending.SetText(strconv.FormatInt(pending, 10))
			ovSyncErr.SetText(syncErr)
			versionFooter.SetText(fmt.Sprintf("app %s   fw %s   launcher %s   game %s   p%d",
				v.App, v.Framework, v.Launcher, nonEmpty(v.Game, "-"), v.Protocol))
		}
	}

	rebuildControls()
	reloadTelemetry()
	reloadForms()
	reloadBoards()
	reloadDevices()
	computeLive()()

	doneUI := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				fyne.Do(func() { a.Quit() })
				return
			case <-doneUI:
				return
			case <-ticker.C:
				if uiStarted.Load() {
					apply := computeLive()
					fyne.Do(apply)
				}
			}
		}
	}()

	footer := container.NewVBox(
		widget.NewSeparator(),
		container.NewPadded(container.NewHBox(statusLabel, layout.NewSpacer(), versionFooter)),
	)

	w.SetOnClosed(func() {
		close(doneUI)
		sup.Stop()
	})
	w.SetContent(container.NewBorder(topBar, footer, nil, nil, tabs))
	w.ShowAndRun()
	return nil
}

func statusChip(content fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 24, G: 29, B: 40, A: 255})
	bg.CornerRadius = 6
	bg.StrokeColor = color.NRGBA{R: 42, G: 49, B: 64, A: 255}
	bg.StrokeWidth = 1
	return container.NewStack(bg, container.NewPadded(content))
}

// sectionPanel renders a clean titled container without verbose card subtitles.
func sectionPanel(title string, content fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 22, G: 26, B: 35, A: 255})
	bg.CornerRadius = 8
	bg.StrokeColor = color.NRGBA{R: 42, G: 49, B: 64, A: 255}
	bg.StrokeWidth = 1

	hdr := canvas.NewText(title, color.NRGBA{R: 156, G: 168, B: 190, A: 255})
	hdr.TextStyle = fyne.TextStyle{Bold: true}
	hdr.TextSize = 12

	body := container.NewPadded(container.NewVBox(hdr, widget.NewSeparator(), content))
	return container.NewStack(bg, body)
}

func settingTag(sp ckframework.SettingSpec) string {
	parts := []string{}
	if sp.Scope == ckframework.ScopeRuntime {
		parts = append(parts, "runtime")
	}
	if sp.Apply == ckframework.ApplyNextRound {
		parts = append(parts, "next round")
	} else if sp.Apply == ckframework.ApplyRestart {
		parts = append(parts, "restart")
	}
	return strings.Join(parts, ", ")
}

func formatUnit(v float64, unit string) string {
	if unit == "" {
		return fmt.Sprintf("%g", v)
	}
	return fmt.Sprintf("%g %s", v, unit)
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
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
