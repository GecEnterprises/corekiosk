package cklauncher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/GecEnterprises/corekiosk/go/ckframework"
	"github.com/GecEnterprises/corekiosk/go/ckserver"
)

func pickAndStoreImageAsset(w fyne.Window, srv *ckserver.Server, setStatus func(string), onUploaded func(ckframework.AssetMeta)) {
	fd := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if rc == nil {
			return
		}
		defer rc.Close()

		raw, readErr := io.ReadAll(io.LimitReader(rc, ckframework.MaxAssetBytes+1))
		if readErr != nil {
			dialog.ShowError(readErr, w)
			return
		}
		meta, putErr := srv.Store().PutAsset(context.Background(), rc.URI().Name(), "", raw, time.Now().UnixMilli())
		if putErr != nil {
			dialog.ShowError(putErr, w)
			return
		}
		setStatus(fmt.Sprintf("Uploaded %s (%dx%d)", meta.Filename, meta.Width, meta.Height))
		onUploaded(meta)
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".png", ".jpg", ".jpeg", ".webp", ".gif"}))
	fd.Show()
}

func makeAssetThumbnail(srv *ckserver.Server, assetID string, size float32) fyne.CanvasObject {
	if strings.TrimSpace(assetID) == "" {
		lbl := widget.NewLabel("No image")
		return lbl
	}
	meta, data, err := srv.Store().GetAsset(context.Background(), assetID)
	if err != nil || len(data) == 0 {
		return widget.NewLabel(assetID[:min(10, len(assetID))])
	}
	// Fyne decodes PNG/JPEG/GIF natively via image.Decode.
	if meta.MimeType == "image/png" || meta.MimeType == "image/jpeg" || meta.MimeType == "image/gif" {
		img := canvas.NewImageFromReader(bytes.NewReader(data), nonEmpty(meta.Filename, assetID))
		img.FillMode = canvas.ImageFillContain
		img.SetMinSize(fyne.NewSize(size, size))
		return img
	}
	return widget.NewLabel(fmt.Sprintf("[%s]", nonEmpty(meta.Filename, assetID)))
}

func buildCatalogsAndRulesetsTab(w fyne.Window, srv *ckserver.Server, setStatus func(string)) (fyne.CanvasObject, func()) {
	catalogsCol := container.NewVBox()
	rulesetsCol := container.NewVBox()

	var reload func()
	reload = func() {
		spec := srv.Spec()
		catViews := srv.Hub().Catalogs()
		ruleViews := srv.Hub().Rulesets()

		// --- Left Column: Catalogs ("Constant Entries", e.g. Prizes) ---
		catPanels := make([]fyne.CanvasObject, 0, len(spec.Catalogs))
		for _, cSpec := range spec.Catalogs {
			cs := cSpec
			view := catViews[cs.ID]
			rows := make([]fyne.CanvasObject, 0, len(view.Entries)+1)

			openEntryEditor := func(existing *ckframework.CatalogEntry) {
				isNew := existing == nil
				curFields := map[string]any{}
				if existing != nil {
					for k, v := range existing.Fields {
						curFields[k] = v
					}
				}
				formItems := []*widget.FormItem{}
				var idEntry *widget.Entry
				if isNew {
					idEntry = widget.NewEntry()
					idEntry.SetPlaceHolder("Optional slug (e.g. hoodie)")
					formItems = append(formItems, widget.NewFormItem("ID", idEntry))
				}

				getters := map[string]func() any{}
				for _, f := range cs.Fields {
					field := f
					val := curFields[field.Key]
					switch field.Type {
					case ckframework.FieldBool:
						chk := widget.NewCheck("", nil)
						if b, ok := val.(bool); ok {
							chk.SetChecked(b)
						} else if defB, ok := field.Default.(bool); ok {
							chk.SetChecked(defB)
						}
						getters[field.Key] = func() any { return chk.Checked }
						formItems = append(formItems, widget.NewFormItem(field.Label, chk))

					case ckframework.FieldImage:
						curAsset := ""
						if s, ok := val.(string); ok {
							curAsset = s
						}
						assetLbl := widget.NewLabel(nonEmpty(curAsset, "None"))
						assetLbl.Wrapping = fyne.TextWrapBreak
						thumbBox := container.NewStack(makeAssetThumbnail(srv, curAsset, 56))
						uploadBtn := widget.NewButtonWithIcon("Upload Image…", theme.UploadIcon(), func() {
							pickAndStoreImageAsset(w, srv, setStatus, func(meta ckframework.AssetMeta) {
								curAsset = meta.ID
								assetLbl.SetText(nonEmpty(meta.Filename, meta.ID))
								thumbBox.Objects = []fyne.CanvasObject{makeAssetThumbnail(srv, curAsset, 56)}
								thumbBox.Refresh()
							})
						})
						getters[field.Key] = func() any { return curAsset }
						formItems = append(formItems, widget.NewFormItem(field.Label, container.NewBorder(nil, nil, container.NewHBox(thumbBox, uploadBtn), nil, assetLbl)))

					case ckframework.FieldNumber:
						ent := widget.NewEntry()
						if val != nil {
							ent.SetText(fmt.Sprint(val))
						}
						getters[field.Key] = func() any {
							fVal, _ := strconv.ParseFloat(strings.TrimSpace(ent.Text), 64)
							return fVal
						}
						formItems = append(formItems, widget.NewFormItem(field.Label, ent))

					default:
						ent := widget.NewEntry()
						ent.SetPlaceHolder(field.Placeholder)
						if val != nil {
							ent.SetText(fmt.Sprint(val))
						}
						getters[field.Key] = func() any { return ent.Text }
						formItems = append(formItems, widget.NewFormItem(field.Label, ent))
					}
				}

				dlgTitle := "Edit " + cs.Title
				if isNew {
					dlgTitle = "Add to " + cs.Title
				}
				d := dialog.NewForm(dlgTitle, "Save", "Cancel", formItems, func(ok bool) {
					if !ok {
						return
					}
					nextFields := map[string]any{}
					for k, fn := range getters {
						nextFields[k] = fn()
					}
					if isNew {
						newEntry := ckframework.CatalogEntry{
							ID:      strings.TrimSpace(idEntry.Text),
							Enabled: true,
							Fields:  nextFields,
						}
						if _, _, err := srv.Hub().AddCatalogEntry(context.Background(), cs.ID, newEntry, "launcher"); err != nil {
							dialog.ShowError(err, w)
							return
						}
						setStatus("Added item to " + cs.Title)
					} else {
						if _, _, err := srv.Hub().PatchCatalogEntry(context.Background(), cs.ID, existing.ID, ckserver.CatalogEntryPatch{
							Fields: nextFields,
						}, "launcher"); err != nil {
							dialog.ShowError(err, w)
							return
						}
						setStatus("Updated " + cs.EntryTitle(*existing))
					}
					reload()
				}, w)
				d.Resize(fyne.NewSize(520, 360))
				d.Show()
			}

			for _, entry := range view.Entries {
				ent := entry
				thumb := makeAssetThumbnail(srv, cs.EntryImageID(ent), 48)
				titleLbl := widget.NewLabel(cs.EntryTitle(ent))
				titleLbl.TextStyle = fyne.TextStyle{Bold: true}
				titleLbl.Wrapping = fyne.TextWrapBreak
				subLbl := widget.NewLabel("id: " + ent.ID)
				subLbl.TextStyle = fyne.TextStyle{Monospace: true}
				subLbl.Truncation = fyne.TextTruncateEllipsis

				enabledChk := widget.NewCheck("Active", nil)
				enabledChk.SetChecked(ent.Enabled)
				enabledChk.OnChanged = func(checked bool) {
					if _, _, err := srv.Hub().PatchCatalogEntry(context.Background(), cs.ID, ent.ID, ckserver.CatalogEntryPatch{
						Enabled: &checked,
					}, "launcher"); err != nil {
						dialog.ShowError(err, w)
						return
					}
					reload()
				}

				uploadQuickBtn := widget.NewButtonWithIcon("Image", theme.UploadIcon(), func() {
					imgKey := cs.ImageField
					if imgKey == "" {
						openEntryEditor(&ent)
						return
					}
					pickAndStoreImageAsset(w, srv, setStatus, func(meta ckframework.AssetMeta) {
						if _, _, err := srv.Hub().PatchCatalogEntry(context.Background(), cs.ID, ent.ID, ckserver.CatalogEntryPatch{
							Fields: map[string]any{imgKey: meta.ID},
						}, "launcher"); err != nil {
							dialog.ShowError(err, w)
							return
						}
						reload()
					})
				})

				editBtn := widget.NewButtonWithIcon("Edit", theme.DocumentCreateIcon(), func() {
					openEntryEditor(&ent)
				})

				actionBtns := []fyne.CanvasObject{enabledChk, uploadQuickBtn, editBtn}
				if cs.AllowAddDelete {
					delBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
						dialog.ShowConfirm("Delete Entry", "Remove "+cs.EntryTitle(ent)+" from "+cs.Title+"?", func(ok bool) {
							if !ok {
								return
							}
							if _, err := srv.Hub().DeleteCatalogEntry(context.Background(), cs.ID, ent.ID, "launcher"); err != nil {
								dialog.ShowError(err, w)
								return
							}
							setStatus("Deleted " + ent.ID)
							reload()
						}, w)
					})
					delBtn.Importance = widget.DangerImportance
					actionBtns = append(actionBtns, delBtn)
				}

				row := container.NewBorder(
					nil,
					widget.NewSeparator(),
					thumb,
					container.NewHBox(actionBtns...),
					container.NewVBox(titleLbl, subLbl),
				)
				rows = append(rows, row)
			}

			if cs.AllowAddDelete {
				addBtn := widget.NewButtonWithIcon("Add Entry", theme.ContentAddIcon(), func() {
					openEntryEditor(nil)
				})
				addBtn.Importance = widget.HighImportance
				rows = append(rows, container.NewHBox(layout.NewSpacer(), addBtn))
			}
			catPanels = append(catPanels, sectionPanel(fmt.Sprintf("%s (%d)", cs.Title, len(view.Entries)), container.NewVBox(rows...)))
		}
		if len(catPanels) == 0 {
			catPanels = append(catPanels, widget.NewLabel("No catalogs defined."))
		}
		catalogsCol.Objects = catPanels
		catalogsCol.Refresh()

		// --- Right Column: Rulesets (e.g. Score Tiers -> Prize Mapping) ---
		rulePanels := make([]fyne.CanvasObject, 0, len(spec.Rulesets))
		for _, rSpec := range spec.Rulesets {
			rs := rSpec
			rView := ruleViews[rs.ID]
			linkedCatSpec, hasCat := spec.CatalogByID(rs.CatalogID)
			linkedCatView := catViews[rs.CatalogID]

			catLabels := []string{}
			idByLabel := map[string]string{}
			labelByID := map[string]string{}
			if hasCat {
				for _, ce := range linkedCatView.Entries {
					lbl := linkedCatSpec.EntryTitle(ce)
					if !ce.Enabled {
						lbl += " (inactive)"
					}
					// Disambiguate duplicate titles with ID suffix if needed
					if _, exists := idByLabel[lbl]; exists {
						lbl = fmt.Sprintf("%s [%s]", lbl, ce.ID)
					}
					catLabels = append(catLabels, lbl)
					idByLabel[lbl] = ce.ID
					labelByID[ce.ID] = lbl
				}
			}

			draft := ckframework.CloneRules(rView.Rules)
			saveDraft := func(next []ckframework.RuleEntry) {
				if _, err := srv.Hub().SaveRuleset(context.Background(), rs.ID, -1, next, "launcher"); err != nil {
					dialog.ShowError(err, w)
					return
				}
				setStatus("Updated " + rs.Title)
				reload()
			}

			rows := make([]fyne.CanvasObject, 0, len(draft)+1)
			for idx := range draft {
				i := idx
				rule := draft[i]

				rangeStr := fmt.Sprintf(">= %.0f pts", rule.MinScore)
				if rs.Kind == ckframework.RulesetScoreTiers {
					if i == 0 {
						rangeStr = fmt.Sprintf("%.0f+ pts", rule.MinScore)
					} else {
						upper := draft[i-1].MinScore - 1
						if upper < rule.MinScore {
							upper = rule.MinScore
						}
						rangeStr = fmt.Sprintf("%.0f – %.0f pts", rule.MinScore, upper)
					}
				}

				tierBadge := widget.NewLabel(fmt.Sprintf("Tier %d (%s)", i+1, rangeStr))
				tierBadge.TextStyle = fyne.TextStyle{Bold: true, Monospace: true}

				minInput := widget.NewEntry()
				minInput.SetText(fmt.Sprintf("%.0f", rule.MinScore))
				minInput.OnSubmitted = func(txt string) {
					if v, err := strconv.ParseFloat(strings.TrimSpace(txt), 64); err == nil {
						next := ckframework.CloneRules(rView.Rules)
						if i < len(next) {
							next[i].MinScore = v
							saveDraft(next)
						}
					}
				}
				saveMinBtn := widget.NewButtonWithIcon("Set Min", theme.ConfirmIcon(), func() {
					if v, err := strconv.ParseFloat(strings.TrimSpace(minInput.Text), 64); err == nil {
						next := ckframework.CloneRules(rView.Rules)
						if i < len(next) {
							next[i].MinScore = v
							saveDraft(next)
						}
					}
				})

				var targetControl fyne.CanvasObject
				if hasCat && len(catLabels) > 0 {
					sel := widget.NewSelect(catLabels, nil)
					sel.Selected = labelByID[rule.EntryID]
					sel.OnChanged = func(chosen string) {
						if newID, ok := idByLabel[chosen]; ok && newID != rule.EntryID {
							next := ckframework.CloneRules(rView.Rules)
							if i < len(next) {
								next[i].EntryID = newID
								saveDraft(next)
							}
						}
					}
					targetControl = sel
				} else {
					lblIn := widget.NewEntry()
					lblIn.SetText(rule.Label)
					lblIn.OnSubmitted = func(txt string) {
						next := ckframework.CloneRules(rView.Rules)
						if i < len(next) {
							next[i].Label = txt
							saveDraft(next)
						}
					}
					targetControl = lblIn
				}

				rightControls := []fyne.CanvasObject{
					widget.NewLabel("Min:"),
					minInput,
					saveMinBtn,
					targetControl,
				}
				if rs.AllowAddDelete && len(draft) > 1 {
					delBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
						next := append([]ckframework.RuleEntry{}, rView.Rules[:i]...)
						next = append(next, rView.Rules[i+1:]...)
						saveDraft(next)
					})
					delBtn.Importance = widget.DangerImportance
					rightControls = append(rightControls, delBtn)
				}

				rows = append(rows, container.NewBorder(
					nil,
					widget.NewSeparator(),
					tierBadge,
					container.NewHBox(rightControls...),
				))
			}

			if rs.AllowAddDelete {
				addTierBtn := widget.NewButtonWithIcon("Add Tier", theme.ContentAddIcon(), func() {
					defaultEntryID := ""
					if len(linkedCatView.Entries) > 0 {
						defaultEntryID = linkedCatView.Entries[0].ID
					}
					nextScore := 0.0
					if len(rView.Rules) > 0 {
						nextScore = rView.Rules[0].MinScore + 100
					}
					newRule := ckframework.RuleEntry{
						ID:       fmt.Sprintf("tier_%d", time.Now().UnixMilli()%100000),
						Label:    fmt.Sprintf("Tier %d", len(rView.Rules)+1),
						Enabled:  true,
						MinScore: nextScore,
						EntryID:  defaultEntryID,
					}
					next := append(ckframework.CloneRules(rView.Rules), newRule)
					saveDraft(next)
				})
				rows = append(rows, container.NewHBox(layout.NewSpacer(), addTierBtn))
			}

			rulePanels = append(rulePanels, sectionPanel(fmt.Sprintf("%s (%d)", rs.Title, len(rView.Rules)), container.NewVBox(rows...)))
		}
		if len(rulePanels) == 0 {
			rulePanels = append(rulePanels, widget.NewLabel("No rulesets defined."))
		}
		rulesetsCol.Objects = rulePanels
		rulesetsCol.Refresh()
	}

	split := container.NewHSplit(
		container.NewVScroll(container.NewPadded(catalogsCol)),
		container.NewVScroll(container.NewPadded(rulesetsCol)),
	)
	split.Offset = 0.50
	return split, reload
}
