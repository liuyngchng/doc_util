package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/lang"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"pdftoword/internal/ocr"
	"pdftoword/internal/pdfconv"
)

type pdfFile struct {
	path string
	name string
}

// supportedExts 列出所有可转换的文件扩展名（PDF + 常见图片格式）。
var supportedExts = []string{
	".pdf",
	".jpg", ".jpeg", ".png", ".bmp", ".gif", ".tiff", ".tif", ".webp",
}

// isSupportedFile 判断文件是否为可转换格式（扩展名不区分大小写）。
func isSupportedFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range supportedExts {
		if ext == e {
			return true
		}
	}
	return false
}

// isPDF 判断文件是否为 PDF。
func isPDF(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".pdf")
}

func main() {
	// 单实例锁——避免多个实例同时读写 preferences.json 导致 EOF 报错。
	unlock, err := acquireSingleInstanceLock()
	if err != nil {
		fyne.LogError("单实例锁获取失败", err)
		os.Exit(1)
	}
	defer unlock()

	// 强制使用中文界面
	os.Setenv("LANG", "zh_CN.UTF-8")
	os.Setenv("LC_ALL", "zh_CN.UTF-8")
	lang.AddTranslationsForLocale([]byte(`{}`), fyne.Locale("zh"))

	// 自定义主题：加大文件选择对话框中"取消"和"打开"按钮之间的间距
	customTheme := &spacedTheme{Theme: theme.DefaultTheme()}

	a := app.NewWithID("com.pdf2docx.app")
	a.Settings().SetTheme(customTheme)
	w := a.NewWindow("PDF2Word - PDF转Word工具")

	// ── Persistent preferences ──────────────────────────────────────
	prefs := a.Preferences()

	// ── data ────────────────────────────────────────────────────────
	var files []pdfFile
	var converting bool

	// Declare widgets that reference each other in closures
	var fileList *widget.List
	var countLabel *widget.Label
	var convertBtn *widget.Button
	var screenshotCheck *widget.Check
	var textCheck *widget.Check
	var hotwordsEntry *widget.Entry

	// ── OCR model detection (for hint text only) ────────────────────
	baseDir, _ := os.Getwd()
	if exe, err := os.Executable(); err == nil {
		baseDir = filepath.Dir(exe)
	}
	modelsExist := ocr.CheckModels(baseDir)

	// Whether at least one output mode is selected.
	hasOutputMode := func() bool {
		return screenshotCheck.Checked || textCheck.Checked
	}

	// ── file list ───────────────────────────────────────────────────
	fileList = widget.NewList(
		func() int { return len(files) },
		func() fyne.CanvasObject {
			label := widget.NewLabel("filename.pdf")
			label.Wrapping = fyne.TextWrapBreak
			removeBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
			return container.NewBorder(nil, nil, nil, removeBtn, label)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			row := o.(*fyne.Container)
			row.Objects[0].(*widget.Label).SetText(files[id].name)
			removeBtn := row.Objects[1].(*widget.Button)
			removeBtn.OnTapped = func() {
				files = append(files[:id], files[id+1:]...)
				fileList.Refresh()
				updateCountLabel(countLabel, len(files))
				updateConvertBtn(convertBtn, len(files), converting, hasOutputMode())
			}
		},
	)
	fileListScroll := container.NewScroll(fileList)
	fileListScroll.SetMinSize(fyne.NewSize(0, 200))

	// ── header / controls ───────────────────────────────────────────
	countLabel = widget.NewLabel("已选文件: 0 个")
	updateCountLabel(countLabel, 0)

	// status / output labels — defined early because addBtn / clearBtn closures reference them.
	statusLabel := widget.NewLabel("请添加 PDF 或图片文件")
	statusLabel.Alignment = fyne.TextAlignCenter

	outputLabel := widget.NewLabel("")
	outputLabel.Wrapping = fyne.TextWrapBreak

	addBtn := widget.NewButtonWithIcon("添加文件", theme.FileIcon(), func() {
		showFilePicker(w, &files, fileList, countLabel, convertBtn, converting, hasOutputMode, statusLabel, outputLabel)
	})

	addFolderBtn := widget.NewButtonWithIcon("添加文件夹", theme.FolderOpenIcon(), func() {
		showFolderPicker(w, &files, fileList, countLabel, convertBtn, converting, hasOutputMode, statusLabel, outputLabel)
	})

	clearBtn := widget.NewButtonWithIcon("清空列表", theme.DeleteIcon(), func() {
		files = nil
		fileList.Refresh()
		updateCountLabel(countLabel, 0)
		updateConvertBtn(convertBtn, 0, converting, hasOutputMode())
		statusLabel.SetText("请添加 PDF 或图片文件")
		outputLabel.SetText("")
		w.SetTitle("PDF2Word - PDF转Word工具")
	})

	btnRow := container.NewHBox(addBtn, addFolderBtn)
	topBar := container.NewBorder(nil, nil, btnRow, clearBtn, countLabel)

	// ── bottom ──────────────────────────────────────────────────────
	progressBar := widget.NewProgressBar()
	progressBar.Hide()

	// ── Output mode checkboxes ──────────────────────────────────────
	screenshotCheck = widget.NewCheck("生成截图版 (.docx)", func(enabled bool) {
		prefs.SetBool("screenshotMode", enabled)
		updateConvertBtn(convertBtn, len(files), converting, hasOutputMode())
		if hotwordsEntry != nil {
			if enabled {
				hotwordsEntry.Enable()
			} else {
				hotwordsEntry.Disable()
			}
		}
	})

	textCheck = widget.NewCheck("生成文字版 (.text.docx)", func(enabled bool) {
		prefs.SetBool("textMode", enabled)
		updateConvertBtn(convertBtn, len(files), converting, hasOutputMode())
	})

	hotwordsEntry = widget.NewEntry()
	hotwordsEntry.SetPlaceHolder("热词（用逗号、分号或换行分隔）")
	hotwordsEntry.MultiLine = true
	hotwordsEntry.Wrapping = fyne.TextWrapBreak
	hotwordsEntry.SetMinRowsVisible(2)
	hotwordsEntry.OnChanged = func(s string) {
		prefs.SetString("hotwords", s)
	}
	hotwordLabel := widget.NewLabel("截图版热词标注（红色下划线）：")
	hotwordRow := container.NewBorder(
		container.NewVBox(hotwordLabel),
		nil, nil, nil,
		hotwordsEntry,
	)

	var textHint *widget.Label
	if modelsExist {
		textHint = widget.NewLabel("（已检测到 OCR 模型，扫描件也能识别文字）")
	} else {
		textHint = widget.NewLabel("（未检测到 OCR 模型，扫描件无法识别文字；有文字层的 PDF 仍可提取文字）")
	}

	modeRow := container.NewVBox(
		container.NewHBox(screenshotCheck),
		hotwordRow,
		container.NewHBox(textCheck, textHint),
	)

	convertBtn = widget.NewButtonWithIcon("开始转换", theme.MediaPlayIcon(), func() {
		if len(files) == 0 || !hasOutputMode() {
			return
		}

		// Check: image files + hotwords + no OCR model → warn the user.
		hotwords := pdfconv.ParseHotwords(hotwordsEntry.Text)
		if len(hotwords) > 0 && !modelsExist {
			hasImage := false
			for _, f := range files {
				if !isPDF(f.path) {
					hasImage = true
					break
				}
			}
			if hasImage {
				dialog.ShowInformation(
					"热词标注不可用",
					"文件列表中包含图片文件（非 PDF）。\n\n"+
						"图片文件的热词标注需要 OCR 模型，但当前未检测到 OCR 模型目录（models/）。\n\n"+
						"请先安装 OCR 模型，或从列表中移除图片文件后重试。",
					w,
				)
				return
			}
		}

		converting = true
		updateConvertBtn(convertBtn, len(files), converting, hasOutputMode())
		screenshotCheck.Disable()
		textCheck.Disable()
		hotwordsEntry.Disable()

		progressBar.Show()
		progressBar.SetValue(0)
		outputLabel.SetText("")

		wantScreenshot := screenshotCheck.Checked
		wantText := textCheck.Checked

		go func() {
			var results []string
			total := len(files)
			for i, f := range files {
				fyne.Do(func() {
					statusLabel.SetText(fmt.Sprintf("正在处理 (%d/%d): %s", i+1, total, f.name))
					w.SetTitle(fmt.Sprintf("PDF2Word - 转换中 (%d/%d)", i+1, total))
				})

				baseProgress := float64(i) / float64(total)

				// Screenshot (image) mode — always works.
				if wantScreenshot {
					var err error
					if len(hotwords) > 0 && isPDF(f.path) {
						_, err = pdfconv.ConvertWithHotwords(f.path, pdfconv.ModeImage, hotwords, func(pct float64) {
							fyne.Do(func() { progressBar.SetValue(baseProgress + pct/float64(total)) })
						})
					} else if len(hotwords) > 0 && !isPDF(f.path) {
						_, err = pdfconv.ConvertImageWithHotwords(f.path, pdfconv.ModeImage, hotwords, func(pct float64) {
							fyne.Do(func() { progressBar.SetValue(baseProgress + pct/float64(total)) })
						})
					} else {
						_, err = pdfconv.Convert(f.path, pdfconv.ModeImage, func(pct float64) {
							fyne.Do(func() { progressBar.SetValue(baseProgress + pct/float64(total)) })
						})
					}
					if err != nil {
						fyne.Do(func() {
							dialog.ShowError(fmt.Errorf("截图版转换 %s 失败: %w", f.name, err), w)
						})
					}
				}

				// Text mode — extract / OCR.
				if wantText {
					if _, err := pdfconv.Convert(f.path, pdfconv.ModeText, func(pct float64) {
						fyne.Do(func() { progressBar.SetValue(baseProgress + pct/float64(total)) })
					}); err != nil {
						fyne.Do(func() {
							dialog.ShowError(fmt.Errorf("文字版转换 %s 失败: %w", f.name, err), w)
						})
					}
				}

				fyne.Do(func() {
					results = append(results, "✓ "+truncateName(f.name, 30))
					progressBar.SetValue(float64(i+1) / float64(total))
				})
			}

			fyne.Do(func() {
				progressBar.Hide()
				statusLabel.SetText("全部转换完成！")
				outputLabel.SetText(strings.Join(results, "\n"))
				w.SetTitle("PDF2Word - 完成")

				msgLabel := widget.NewLabel(fmt.Sprintf("共 %d 个文件:\n\n%s", total, strings.Join(results, "\n")))
				msgLabel.Alignment = fyne.TextAlignLeading
				d := dialog.NewCustom("批量转换完成", "确定", msgLabel, w)
				d.Show()

				converting = false
				screenshotCheck.Enable()
				textCheck.Enable()
				if screenshotCheck.Checked {
					hotwordsEntry.Enable()
				}
				updateConvertBtn(convertBtn, len(files), converting, hasOutputMode())
			})
		}()
	})
	convertBtn.Disable()

	// 恢复用户上次的选择（必须在 convertBtn 创建后调用，否则回调里
	// updateConvertBtn 会拿到 nil 的 convertBtn）。
	screenshotCheck.SetChecked(prefs.BoolWithFallback("screenshotMode", false))
	textCheck.SetChecked(prefs.BoolWithFallback("textMode", true))
	hotwordsEntry.SetText(prefs.StringWithFallback("hotwords", ""))
	if !screenshotCheck.Checked {
		hotwordsEntry.Disable()
	}

	convertBar := container.NewVBox(
		statusLabel,
		progressBar,
		outputLabel,
		modeRow,
		convertBtn,
	)

	// ── layout ──────────────────────────────────────────────────────
	content := container.NewBorder(
		topBar,
		convertBar,
		nil, nil,
		fileListScroll,
	)

	w.SetContent(content)
	w.Resize(fyne.NewSize(800, 600))
	w.CenterOnScreen()
	// 监听 SIGINT (Ctrl+C) / SIGTERM，支持从终端优雅退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fyne.Do(func() { a.Quit() })
	}()

	w.ShowAndRun()
}

func showFolderPicker(w fyne.Window, files *[]pdfFile, fileList *widget.List, countLabel *widget.Label, convertBtn *widget.Button, converting bool, hasOutputMode func() bool, statusLabel *widget.Label, outputLabel *widget.Label) {
	fd := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if uri == nil {
			return // cancelled
		}

		folderPath := uri.Path()
		added := 0
		skipped := 0

		// Walk the selected folder (non-recursive by default).
		entries, err := os.ReadDir(folderPath)
		if err != nil {
			dialog.ShowError(fmt.Errorf("无法读取文件夹: %w", err), w)
			return
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if !isSupportedFile(entry.Name()) {
				continue
			}

			filePath := filepath.Join(folderPath, entry.Name())

			// dedup
			dup := false
			for _, f := range *files {
				if f.path == filePath {
					dup = true
					break
				}
			}
			if dup {
				skipped++
				continue
			}

			*files = append(*files, pdfFile{path: filePath, name: entry.Name()})
			added++
		}

		if added == 0 && skipped == 0 {
			dialog.ShowInformation("未找到可转换文件", "该文件夹中没有 PDF 或图片文件", w)
			return
		}

		fyne.Do(func() {
			fileList.Refresh()
			updateCountLabel(countLabel, len(*files))
			updateConvertBtn(convertBtn, len(*files), converting, hasOutputMode())
			statusLabel.SetText("请点击「开始转换」")
			outputLabel.SetText("")
			fileList.ScrollToBottom()
		})

		if skipped > 0 {
			fyne.Do(func() {
				dialog.ShowInformation("提示", fmt.Sprintf("已添加 %d 个文件，跳过 %d 个重复文件", added, skipped), w)
			})
		}
	}, w)

	fd.Show()
	fd.Resize(fyne.NewSize(800, 600))
}

func showFilePicker(w fyne.Window, files *[]pdfFile, fileList *widget.List, countLabel *widget.Label, convertBtn *widget.Button, converting bool, hasOutputMode func() bool, statusLabel *widget.Label, outputLabel *widget.Label) {
	fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			dialog.ShowError(err, w)
			return
		}
		if reader == nil {
			return // cancelled
		}

		filePath := reader.URI().Path()
		reader.Close()

		// dedup
		for _, f := range *files {
			if f.path == filePath {
				dialog.ShowInformation("重复文件", "该文件已在列表中", w)
				return
			}
		}

		*files = append(*files, pdfFile{path: filePath, name: filepath.Base(filePath)})

		// OnClosed may fire from a non-UI goroutine; ensure widget
		// updates happen on the Fyne canvas goroutine.
		fyne.Do(func() {
			fileList.Refresh()
			updateCountLabel(countLabel, len(*files))
			updateConvertBtn(convertBtn, len(*files), converting, hasOutputMode())
			// 新加文件时清除之前的转换结果
			statusLabel.SetText("请点击「开始转换」")
			outputLabel.SetText("")

			// Scroll to bottom
			fileList.ScrollToBottom()
		})
	}, w)

	fd.SetFilter(storage.NewExtensionFileFilter(supportedExts))
	fd.Show()
	fd.Resize(fyne.NewSize(800, 600))
}

func truncateName(name string, maxChars int) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)

	runes := []rune(base)
	extRunes := []rune(ext)

	if len(runes)+len(extRunes) <= maxChars {
		return name
	}

	// Reserve space for "..." and extension
	keep := maxChars - 3 - len(extRunes)
	if keep < 1 {
		keep = 1
	}
	return string(runes[:keep]) + "..." + ext
}

func updateCountLabel(l *widget.Label, n int) {
	l.SetText(fmt.Sprintf("已选文件: %d 个", n))
}

func updateConvertBtn(btn *widget.Button, n int, converting bool, hasMode bool) {
	if converting {
		btn.Disable()
	} else if n == 0 || !hasMode {
		btn.Disable()
	} else {
		btn.Enable()
	}
}

// spacedTheme 在默认主题基础上，加大元素之间的 Padding 间距。
// Fyne 的文件选择对话框用 GridWithRows 排布"取消"和"打开"按钮，
// 按钮间距取自 theme.Padding()，覆盖该值即可加大两个按钮之间的距离。
type spacedTheme struct {
	fyne.Theme
}

func (t *spacedTheme) Size(name fyne.ThemeSizeName) float32 {
	if name == theme.SizeNamePadding {
		return 16 // 默认 4，加大到 16
	}
	return t.Theme.Size(name)
}