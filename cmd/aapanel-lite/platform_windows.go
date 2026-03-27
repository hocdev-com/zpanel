//go:build windows

package main

import (
	"context"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func setHideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	gdi32                = windows.NewLazySystemDLL("gdi32.dll")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procDrawTextW        = user32.NewProc("DrawTextW")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procEndPaint         = user32.NewProc("EndPaint")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procFillRect         = user32.NewProc("FillRect")
	procFindWindowW      = user32.NewProc("FindWindowW")
	procGetDC            = user32.NewProc("GetDC")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procSetForegroundW   = user32.NewProc("SetForegroundWindow")
	procSetMenuItemBmpW  = user32.NewProc("SetMenuItemBitmaps")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procRegisterClassW   = user32.NewProc("RegisterClassW")
	procReleaseDC        = user32.NewProc("ReleaseDC")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procSetCursor        = user32.NewProc("SetCursor")
	procShowWindow       = user32.NewProc("ShowWindow")
	procTrackMouseEvent  = user32.NewProc("TrackMouseEvent")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procUpdateWindow     = user32.NewProc("UpdateWindow")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = shell32.NewProc("ShellExecuteW")
	procCloseHandle      = kernel32.NewProc("CloseHandle")
	procCreateMutexW     = kernel32.NewProc("CreateMutexW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procReleaseMutex     = kernel32.NewProc("ReleaseMutex")
	procCreateBmp        = gdi32.NewProc("CreateCompatibleBitmap")
	procCreateDC         = gdi32.NewProc("CreateCompatibleDC")
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procCreatePen        = gdi32.NewProc("CreatePen")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procDeleteDC         = gdi32.NewProc("DeleteDC")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procGetStockObject   = gdi32.NewProc("GetStockObject")
	procLineTo           = gdi32.NewProc("LineTo")
	procMoveToEx         = gdi32.NewProc("MoveToEx")
	procRectangle        = gdi32.NewProc("Rectangle")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
)

const (
	wmDestroy    = 0x0002
	wmClose      = 0x0010
	wmCommand    = 0x0111
	wmEraseBkgnd = 0x0014
	wmPaint      = 0x000F
	wmMouseMove  = 0x0200
	wmMouseLeave = 0x02A3
	wmLButtonUp  = 0x0202
	wmLButtonDbl = 0x0203
	wmSetCursor  = 0x0020
	wmSetIcon    = 0x0080
	wmAppSync    = 0x8001
	wmAppErr     = 0x8002
	wmAppRestore = 0x8003
	wmTrayIcon   = 0x8004

	mfString       = 0x00000000
	mfByCommand    = 0x00000000
	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	wsVisible     = 0x10000000
	wsPanelWindow = wsCaption | wsSysMenu | wsMinimizeBox

	swHide     = 0
	swShow     = 5
	swMinimize = 6
	swRestore  = 9

	iconSmall = 0
	iconBig   = 1

	idcArrow = 32512
	idcHand  = 32649

	idiApplication = 32512

	smCxScreen = 0
	smCyScreen = 1

	mbIconError = 0x00000010
	mbIconWarn  = 0x00000030
	psSolid     = 0

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage  = 0x00000001
	nifIcon     = 0x00000002
	nifTip      = 0x00000004
	hollowBrush = 5

	dtLeft       = 0x0000
	dtCenter     = 0x0001
	dtVCenter    = 0x0004
	dtSingleLine = 0x0020

	transparentBk = 1
	tmeLeave      = 0x00000002
)

const (
	mainWindowClass = "AAPanelLiteControlPanel"
	mainWindowTitle = "aaPanel Lite"
	instanceName    = "Local\\AAPanelLiteSingleton"
	trayIconID      = 1001
	menuShowID      = 2001
	menuOpenID      = 2002
	menuExitID      = 2003
)

type wndClass struct {
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
}

type point struct {
	X int32
	Y int32
}

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type msg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type paintStruct struct {
	Hdc         uintptr
	Erase       int32
	Paint       rect
	Restore     int32
	IncUpdate   int32
	RGBReserved [32]byte
}

type trackMouseEvent struct {
	CbSize    uint32
	DwFlags   uint32
	HwndTrack uintptr
	DwHover   uint32
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeoutOrVer    uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type trayMenuIcons struct {
	Show uintptr
	Open uintptr
	Exit uintptr
}

type uiFontSet struct {
	Title   uintptr
	Body    uintptr
	Small   uintptr
	Mono    uintptr
	Button  uintptr
	Caption uintptr
}

type hoverTarget int

const (
	hoverNone hoverTarget = iota
	hoverLink
	hoverStartStop
	hoverHide
	hoverExit
)

type controlPanelShell struct {
	controller    *serverController
	stop          context.CancelFunc
	dashboardURL  string
	hwnd          uintptr
	appIcon       uintptr
	menuIcons     trayMenuIcons
	fonts         uiFontSet
	mu            sync.Mutex
	busy          bool
	lastErr       string
	trayVisible   bool
	hover         hoverTarget
	trackingMouse bool
}

var (
	activeShell    *controlPanelShell
	instanceHandle uintptr
)

func acquirePlatformInstance() (bool, error) {
	mutexName, _ := syscall.UTF16PtrFromString(instanceName)
	handle, _, callErr := procCreateMutexW.Call(0, 1, uintptr(unsafe.Pointer(mutexName)))
	if handle == 0 {
		return false, callErr
	}

	if callErr == windows.ERROR_ALREADY_EXISTS {
		restoreExistingInstanceWindow()
		procCloseHandle.Call(handle)
		return true, nil
	}

	instanceHandle = handle
	return false, nil
}

func releasePlatformInstance() {
	if instanceHandle != 0 {
		procReleaseMutex.Call(instanceHandle)
		procCloseHandle.Call(instanceHandle)
		instanceHandle = 0
	}
}

func runPlatformShell(ctx context.Context, stop context.CancelFunc, controller *serverController, appRoot string, dashboardURL string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	shell := &controlPanelShell{
		controller:   controller,
		stop:         stop,
		dashboardURL: dashboardURL,
	}
	activeShell = shell
	defer func() {
		activeShell = nil
	}()

	hwnd, err := createMainPanel(shell)
	if err != nil {
		return err
	}
	shell.hwnd = hwnd

	go func() {
		manager := newRuntimeManager(appRoot)
		checker, ok := manager.(runtimeStartupChecker)
		if !ok {
			return
		}
		if err := checker.RunStartupChecks(); err != nil {
			shell.showMessage("Runtime Required", err.Error(), mbIconWarn)
		}
	}()

	go func() {
		<-ctx.Done()
		if shell.hwnd != 0 {
			procPostMessageW.Call(shell.hwnd, wmClose, 0, 0)
		}
	}()

	var message msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}

	return controller.Stop()
}

func createMainPanel(shell *controlPanelShell) (uintptr, error) {
	instance, _, err := procGetModuleHandleW.Call(0)
	if instance == 0 {
		return 0, err
	}

	className, _ := syscall.UTF16PtrFromString(mainWindowClass)
	title, _ := syscall.UTF16PtrFromString(mainWindowTitle)

	cursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))
	icon, _, _ := procLoadIconW.Call(instance, 1)
	if icon == 0 {
		icon, _, _ = procLoadIconW.Call(0, uintptr(idiApplication))
	}
	class := wndClass{
		WndProc:   syscall.NewCallback(windowProc),
		Instance:  windows.Handle(instance),
		Icon:      windows.Handle(icon),
		Cursor:    windows.Handle(cursor),
		ClassName: className,
	}
	procRegisterClassW.Call(uintptr(unsafe.Pointer(&class)))

	hwnd, _, createErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		uintptr(wsPanelWindow|wsVisible),
		uintptr(centeredWindowX(620)),
		uintptr(centeredWindowY(370)),
		620,
		370,
		0,
		0,
		instance,
		0,
	)
	if hwnd == 0 {
		return 0, createErr
	}

	shell.appIcon = icon
	if icon != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconBig, icon)
		procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, icon)
	}

	shell.fonts = uiFontSet{
		Title:   createFont(-28, 700, "Segoe UI Semibold"),
		Body:    createFont(-18, 400, "Segoe UI"),
		Small:   createFont(-15, 400, "Segoe UI"),
		Mono:    createFont(-18, 500, "Consolas"),
		Button:  createFont(-18, 700, "Segoe UI Semibold"),
		Caption: createFont(-14, 600, "Segoe UI"),
	}
	shell.menuIcons = trayMenuIcons{
		Show: createTrayMenuIcon("show", rgb(37, 99, 235)),
		Open: createTrayMenuIcon("open", rgb(15, 118, 110)),
		Exit: createTrayMenuIcon("exit", rgb(185, 28, 28)),
	}

	procShowWindow.Call(hwnd, swShow)
	procUpdateWindow.Call(hwnd)
	return hwnd, nil
}

func windowProc(hwnd uintptr, msg uint32, wParam uintptr, lParam uintptr) uintptr {
	if activeShell == nil {
		result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return result
	}

	switch msg {
	case wmPaint:
		activeShell.paint()
		return 0
	case wmEraseBkgnd:
		return 1
	case wmMouseMove:
		activeShell.onMouseMove(pointFromLParam(lParam))
		return 0
	case wmMouseLeave:
		activeShell.onMouseLeave()
		return 0
	case wmLButtonUp:
		activeShell.onClick(pointFromLParam(lParam))
		return 0
	case wmCommand:
		activeShell.handleMenuCommand(uint32(wParam & 0xffff))
		return 0
	case wmTrayIcon:
		switch uint32(lParam) {
		case wmLButtonUp, wmLButtonDbl:
			activeShell.restoreFromTray()
		case 0x0205:
			activeShell.showTrayMenu()
		}
		return 0
	case wmSetCursor:
		if activeShell.applyCursor() {
			return 1
		}
	case wmAppSync:
		activeShell.finishAsync()
		return 0
	case wmAppErr:
		activeShell.finishAsync()
		activeShell.showLastError()
		return 0
	case wmAppRestore:
		activeShell.restoreFromTray()
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		activeShell.removeTrayIcon()
		activeShell.releaseMenuIcons()
		activeShell.releaseFonts()
		activeShell.stop()
		procPostQuitMessage.Call(0)
		return 0
	}

	result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return result
}

func (s *controlPanelShell) paint() {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(s.hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(s.hwnd, uintptr(unsafe.Pointer(&ps)))

	client := s.clientRect()
	fillRect(hdc, client, rgb(8, 15, 31))
	fillRect(hdc, rect{Left: 0, Top: 0, Right: client.Right, Bottom: 88}, rgb(13, 28, 58))
	fillRect(hdc, rect{Left: 0, Top: 88, Right: client.Right, Bottom: 96}, rgb(20, 108, 214))

	fillRect(hdc, rect{Left: 28, Top: 118, Right: client.Right - 28, Bottom: 196}, rgb(16, 30, 56))
	fillRect(hdc, rect{Left: 28, Top: 212, Right: 236, Bottom: 292}, rgb(16, 30, 56))
	fillRect(hdc, s.quickActionsRect(), rgb(16, 30, 56))

	fillRect(hdc, rect{Left: 36, Top: 20, Right: 94, Bottom: 78}, rgb(36, 116, 255))
	drawText(hdc, "AP", rect{Left: 36, Top: 20, Right: 94, Bottom: 78}, s.fonts.Title, rgb(248, 251, 255), dtCenter|dtVCenter|dtSingleLine)
	drawText(hdc, "aaPanel Lite", rect{Left: 116, Top: 20, Right: client.Right - 24, Bottom: 52}, s.fonts.Title, rgb(248, 251, 255), dtLeft|dtVCenter|dtSingleLine)
	drawText(hdc, "Lightweight local control panel", rect{Left: 116, Top: 52, Right: client.Right - 24, Bottom: 76}, s.fonts.Small, rgb(160, 184, 220), dtLeft|dtVCenter|dtSingleLine)

	drawText(hdc, "Access Link", rect{Left: 44, Top: 128, Right: client.Right - 44, Bottom: 146}, s.fonts.Caption, rgb(125, 176, 255), dtLeft|dtVCenter|dtSingleLine)
	linkColor := rgb(198, 226, 255)
	if s.hover == hoverLink {
		linkColor = rgb(255, 255, 255)
	}
	drawText(hdc, s.dashboardURL, s.linkRect(), s.fonts.Mono, linkColor, dtLeft|dtVCenter|dtSingleLine)
	drawText(hdc, "Click the link to open the dashboard in your browser.", rect{Left: 44, Top: 170, Right: client.Right - 44, Bottom: 188}, s.fonts.Small, rgb(144, 162, 192), dtLeft|dtVCenter|dtSingleLine)

	drawText(hdc, "Panel Status", rect{Left: 44, Top: 224, Right: 200, Bottom: 242}, s.fonts.Caption, rgb(125, 176, 255), dtLeft|dtVCenter|dtSingleLine)
	statusColor := rgb(24, 194, 118)
	statusText := "Running"
	if !s.controller.Running() {
		statusColor = rgb(229, 96, 96)
		statusText = "Stopped"
	}
	if s.isBusy() {
		statusColor = rgb(248, 188, 62)
		statusText = "Processing..."
	}
	fillRect(hdc, rect{Left: 44, Top: 250, Right: 58, Bottom: 264}, statusColor)
	drawText(hdc, statusText, rect{Left: 68, Top: 244, Right: 198, Bottom: 268}, s.fonts.Body, rgb(240, 245, 255), dtLeft|dtVCenter|dtSingleLine)

	drawText(hdc, "Quick Actions", rect{Left: 264, Top: 224, Right: client.Right - 44, Bottom: 242}, s.fonts.Caption, rgb(125, 176, 255), dtLeft|dtVCenter|dtSingleLine)
	drawModernButton(hdc, s.startStopRect(), s.startStopLabel(), s.fonts.Button, s.startStopFill(), darken(s.startStopFill(), 22), rgb(255, 255, 255), false)
	drawModernButton(hdc, s.hideRect(), "Hide", s.fonts.Button, rgb(69, 118, 255), rgb(40, 87, 219), rgb(255, 255, 255), false)
	drawModernButton(hdc, s.exitRect(), "Exit", s.fonts.Button, rgb(27, 154, 143), rgb(17, 125, 116), rgb(255, 255, 255), false)
}

func (s *controlPanelShell) runAsync(action func() error) {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return
	}
	s.busy = true
	s.lastErr = ""
	s.mu.Unlock()
	invalidateWindow(s.hwnd)

	go func() {
		if err := action(); err != nil {
			s.mu.Lock()
			s.lastErr = err.Error()
			s.mu.Unlock()
			procPostMessageW.Call(s.hwnd, wmAppErr, 0, 0)
			return
		}
		procPostMessageW.Call(s.hwnd, wmAppSync, 0, 0)
	}()
}

func (s *controlPanelShell) finishAsync() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
	invalidateWindow(s.hwnd)
}

func (s *controlPanelShell) onMouseMove(pt point) {
	next := hoverNone
	if inside(pt, s.linkRect()) {
		next = hoverLink
	}

	if !s.trackingMouse {
		s.trackingMouse = true
		track := trackMouseEvent{
			CbSize:    uint32(unsafe.Sizeof(trackMouseEvent{})),
			DwFlags:   tmeLeave,
			HwndTrack: s.hwnd,
		}
		procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&track)))
	}

	if next != s.hover {
		s.hover = next
		invalidateWindow(s.hwnd)
	}
}

func (s *controlPanelShell) onMouseLeave() {
	s.trackingMouse = false
	if s.hover != hoverNone {
		s.hover = hoverNone
		invalidateWindow(s.hwnd)
	}
}

func (s *controlPanelShell) onClick(pt point) {
	switch s.hitTest(pt) {
	case hoverLink:
		go s.openDashboard()
	case hoverStartStop:
		s.runAsync(func() error {
			if s.controller.Running() {
				return s.controller.Stop()
			}
			return s.controller.Start()
		})
	case hoverHide:
		s.hideToTray()
	case hoverExit:
		procDestroyWindow.Call(s.hwnd)
	}
}

func (s *controlPanelShell) applyCursor() bool {
	cursorID := idcArrow
	if s.hover == hoverLink {
		cursorID = idcHand
	}
	cursor, _, _ := procLoadCursorW.Call(0, uintptr(cursorID))
	if cursor != 0 {
		procSetCursor.Call(cursor)
		return true
	}
	return false
}

func (s *controlPanelShell) openDashboard() {
	openVerb, _ := syscall.UTF16PtrFromString("open")
	target, _ := syscall.UTF16PtrFromString(s.dashboardURL)
	procShellExecuteW.Call(
		s.hwnd,
		uintptr(unsafe.Pointer(openVerb)),
		uintptr(unsafe.Pointer(target)),
		0,
		0,
		swShow,
	)
}

func (s *controlPanelShell) showTrayMenu() {
	if s.hwnd == 0 {
		return
	}

	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	appendMenuItem(menu, menuShowID, "Show")
	appendMenuItem(menu, menuOpenID, "Open Dashboard")
	appendMenuItem(menu, menuExitID, "Exit")
	setTrayMenuItemBitmap(menu, menuShowID, s.menuIcons.Show)
	setTrayMenuItemBitmap(menu, menuOpenID, s.menuIcons.Open)
	setTrayMenuItemBitmap(menu, menuExitID, s.menuIcons.Exit)

	var pt point
	if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ok == 0 {
		return
	}

	procSetForegroundW.Call(s.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmLeftAlign|tpmRightButton|tpmReturnCmd,
		uintptr(pt.X),
		uintptr(pt.Y),
		0,
		s.hwnd,
		0,
	)
	if cmd != 0 {
		s.handleMenuCommand(uint32(cmd))
	}
	procPostMessageW.Call(s.hwnd, 0, 0, 0)
}

func (s *controlPanelShell) handleMenuCommand(command uint32) {
	switch command {
	case menuShowID:
		s.restoreFromTray()
	case menuOpenID:
		go s.openDashboard()
	case menuExitID:
		procDestroyWindow.Call(s.hwnd)
	}
}

func (s *controlPanelShell) hideToTray() {
	if s.hwnd == 0 {
		return
	}
	if !s.addTrayIcon() {
		return
	}
	s.hover = hoverNone
	procShowWindow.Call(s.hwnd, swHide)
}

func (s *controlPanelShell) restoreFromTray() {
	if s.hwnd == 0 {
		return
	}
	s.removeTrayIcon()
	procShowWindow.Call(s.hwnd, swShow)
	procShowWindow.Call(s.hwnd, swRestore)
	procSetForegroundW.Call(s.hwnd)
	procUpdateWindow.Call(s.hwnd)
}

func (s *controlPanelShell) addTrayIcon() bool {
	s.mu.Lock()
	if s.trayVisible {
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()

	data := s.newTrayIconData()
	ok, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data)))
	if ok == 0 {
		return false
	}

	s.mu.Lock()
	s.trayVisible = true
	s.mu.Unlock()
	return true
}

func (s *controlPanelShell) removeTrayIcon() {
	s.mu.Lock()
	if !s.trayVisible {
		s.mu.Unlock()
		return
	}
	s.trayVisible = false
	s.mu.Unlock()

	data := s.newTrayIconData()
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
}

func (s *controlPanelShell) newTrayIconData() notifyIconData {
	var data notifyIconData
	data.CbSize = uint32(unsafe.Sizeof(data))
	data.HWnd = s.hwnd
	data.UID = trayIconID
	data.UFlags = nifMessage | nifIcon | nifTip
	data.UCallbackMessage = wmTrayIcon
	data.HIcon = s.appIcon
	copyUTF16(data.SzTip[:], mainWindowTitle)
	return data
}

func (s *controlPanelShell) showLastError() {
	s.mu.Lock()
	message := s.lastErr
	s.mu.Unlock()
	if message == "" {
		return
	}
	s.showMessage("aaPanel Lite", message, mbIconError)
}

func (s *controlPanelShell) showMessage(titleText string, message string, flags uintptr) {
	value, _ := syscall.UTF16PtrFromString(message)
	title, _ := syscall.UTF16PtrFromString(titleText)
	procMessageBoxW.Call(s.hwnd, uintptr(unsafe.Pointer(value)), uintptr(unsafe.Pointer(title)), flags)
}

func (s *controlPanelShell) isBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *controlPanelShell) startStopLabel() string {
	if s.isBusy() {
		return "Working..."
	}
	if s.controller.Running() {
		return "Stop"
	}
	return "Start"
}

func (s *controlPanelShell) startStopFill() uint32 {
	if s.isBusy() {
		return rgb(113, 120, 138)
	}
	if s.controller.Running() {
		return rgb(220, 38, 38)
	}
	return rgb(22, 163, 74)
}

func (s *controlPanelShell) releaseFonts() {
	for _, handle := range []uintptr{s.fonts.Title, s.fonts.Body, s.fonts.Small, s.fonts.Mono, s.fonts.Button, s.fonts.Caption} {
		if handle != 0 {
			procDeleteObject.Call(handle)
		}
	}
}

func (s *controlPanelShell) releaseMenuIcons() {
	for _, handle := range []uintptr{s.menuIcons.Show, s.menuIcons.Open, s.menuIcons.Exit} {
		if handle != 0 {
			procDeleteObject.Call(handle)
		}
	}
}

func (s *controlPanelShell) clientRect() rect {
	var bounds rect
	procGetClientRect.Call(s.hwnd, uintptr(unsafe.Pointer(&bounds)))
	return bounds
}

func (s *controlPanelShell) linkRect() rect {
	return rect{Left: 44, Top: 146, Right: s.clientRect().Right - 44, Bottom: 166}
}

func (s *controlPanelShell) startStopRect() rect {
	card := s.quickActionsRect()
	padding := int32(16)
	gap := int32(12)
	top := card.Top + 36
	width := ((card.Right - card.Left) - (padding * 2) - (gap * 2)) / 3
	left := card.Left + padding
	return rect{Left: left, Top: top, Right: left + width, Bottom: top + 36}
}

func (s *controlPanelShell) hideRect() rect {
	start := s.startStopRect()
	gap := int32(12)
	width := start.Right - start.Left
	left := start.Right + gap
	return rect{Left: left, Top: start.Top, Right: left + width, Bottom: start.Bottom}
}

func (s *controlPanelShell) exitRect() rect {
	hide := s.hideRect()
	gap := int32(12)
	width := hide.Right - hide.Left
	left := hide.Right + gap
	return rect{Left: left, Top: hide.Top, Right: left + width, Bottom: hide.Bottom}
}

func (s *controlPanelShell) quickActionsRect() rect {
	client := s.clientRect()
	return rect{Left: 248, Top: 212, Right: client.Right - 28, Bottom: 292}
}

func (s *controlPanelShell) hitTest(pt point) hoverTarget {
	if inside(pt, s.linkRect()) {
		return hoverLink
	}
	if inside(pt, s.startStopRect()) {
		return hoverStartStop
	}
	if inside(pt, s.hideRect()) {
		return hoverHide
	}
	if inside(pt, s.exitRect()) {
		return hoverExit
	}
	return hoverNone
}

func createFont(height int32, weight int32, name string) uintptr {
	fontName, _ := syscall.UTF16PtrFromString(name)
	handle, _, _ := procCreateFontW.Call(
		uintptr(int32(height)),
		0, 0, 0,
		uintptr(weight),
		0, 0, 0,
		1,
		0, 0, 0, 0,
		uintptr(unsafe.Pointer(fontName)),
	)
	return handle
}

func fillRect(hdc uintptr, bounds rect, color uint32) {
	fillSolidRect(hdc, bounds, color)
}

func drawModernButton(hdc uintptr, bounds rect, label string, font uintptr, fill uint32, border uint32, textColor uint32, hovered bool) {
	fillSolidRect(hdc, bounds, border)
	inner := rect{Left: bounds.Left + 1, Top: bounds.Top + 1, Right: bounds.Right - 1, Bottom: bounds.Bottom - 1}
	fillSolidRect(hdc, inner, fill)

	labelRect := inner
	if hovered {
		labelRect = rect{Left: inner.Left, Top: inner.Top - 1, Right: inner.Right, Bottom: inner.Bottom - 1}
	}
	drawText(hdc, label, labelRect, font, textColor, dtCenter|dtVCenter|dtSingleLine)
}

func fillSolidRect(hdc uintptr, bounds rect, color uint32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(color))
	defer procDeleteObject.Call(brush)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&bounds)), brush)
}

func drawText(hdc uintptr, value string, bounds rect, font uintptr, color uint32, flags uintptr) {
	text, _ := syscall.UTF16PtrFromString(value)
	oldFont, _, _ := procSelectObject.Call(hdc, font)
	procSetBkMode.Call(hdc, transparentBk)
	procSetTextColor.Call(hdc, uintptr(color))
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(text)), ^uintptr(0), uintptr(unsafe.Pointer(&bounds)), flags)
	procSelectObject.Call(hdc, oldFont)
}

func pointFromLParam(value uintptr) point {
	return point{
		X: int32(int16(value & 0xffff)),
		Y: int32(int16((value >> 16) & 0xffff)),
	}
}

func inside(pt point, bounds rect) bool {
	return pt.X >= bounds.Left && pt.X <= bounds.Right && pt.Y >= bounds.Top && pt.Y <= bounds.Bottom
}

func invalidateWindow(hwnd uintptr) {
	procInvalidateRect.Call(hwnd, 0, 1)
}

func rgb(r uint8, g uint8, b uint8) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

func darken(color uint32, delta uint8) uint32 {
	r := clampChannel(int(int(color&0xff)) - int(delta))
	g := clampChannel(int(int((color>>8)&0xff)) - int(delta))
	b := clampChannel(int(int((color>>16)&0xff)) - int(delta))
	return rgb(r, g, b)
}

func lighten(color uint32, delta uint8) uint32 {
	r := clampChannel(int(int(color&0xff)) + int(delta))
	g := clampChannel(int(int((color>>8)&0xff)) + int(delta))
	b := clampChannel(int(int((color>>16)&0xff)) + int(delta))
	return rgb(r, g, b)
}

func clampChannel(value int) uint8 {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return uint8(value)
}

func centeredWindowX(width int32) int32 {
	screenW, _, _ := procGetSystemMetrics.Call(smCxScreen)
	return (int32(screenW) - width) / 2
}

func centeredWindowY(height int32) int32 {
	screenH, _, _ := procGetSystemMetrics.Call(smCyScreen)
	return (int32(screenH) - height) / 2
}

func restoreExistingInstanceWindow() {
	className, _ := syscall.UTF16PtrFromString(mainWindowClass)
	for i := 0; i < 15; i++ {
		hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
		if hwnd != 0 {
			procPostMessageW.Call(hwnd, wmAppRestore, 0, 0)
			return
		}
		time.Sleep(120 * time.Millisecond)
	}
}

func copyUTF16(dst []uint16, value string) {
	encoded, _ := syscall.UTF16FromString(value)
	copy(dst, encoded)
}

func appendMenuItem(menu uintptr, id uintptr, label string) {
	value, _ := syscall.UTF16PtrFromString(label)
	procAppendMenuW.Call(menu, mfString, id, uintptr(unsafe.Pointer(value)))
}

func setTrayMenuItemBitmap(menu uintptr, id uintptr, bitmap uintptr) {
	if menu == 0 || bitmap == 0 {
		return
	}
	procSetMenuItemBmpW.Call(menu, id, mfByCommand, bitmap, 0)
}

func createTrayMenuIcon(kind string, color uint32) uintptr {
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return 0
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateDC.Call(screenDC)
	if memDC == 0 {
		return 0
	}
	defer procDeleteDC.Call(memDC)

	bitmap, _, _ := procCreateBmp.Call(screenDC, 16, 16)
	if bitmap == 0 {
		return 0
	}

	oldBitmap, _, _ := procSelectObject.Call(memDC, bitmap)
	defer procSelectObject.Call(memDC, oldBitmap)

	fillSolidRect(memDC, rect{Left: 0, Top: 0, Right: 16, Bottom: 16}, rgb(255, 255, 255))

	pen, _, _ := procCreatePen.Call(psSolid, 2, uintptr(color))
	if pen == 0 {
		return bitmap
	}
	defer procDeleteObject.Call(pen)

	oldPen, _, _ := procSelectObject.Call(memDC, pen)
	defer procSelectObject.Call(memDC, oldPen)

	hollow, _, _ := procGetStockObject.Call(hollowBrush)
	oldBrush, _, _ := procSelectObject.Call(memDC, hollow)
	defer procSelectObject.Call(memDC, oldBrush)

	switch kind {
	case "show":
		procRectangle.Call(memDC, 3, 3, 13, 11)
		moveLine(memDC, 3, 5, 13, 5)
	case "open":
		procRectangle.Call(memDC, 3, 7, 10, 13)
		moveLine(memDC, 7, 9, 12, 4)
		moveLine(memDC, 9, 4, 12, 4)
		moveLine(memDC, 12, 4, 12, 7)
	case "exit":
		moveLine(memDC, 4, 4, 12, 12)
		moveLine(memDC, 12, 4, 4, 12)
	}

	return bitmap
}

func moveLine(hdc uintptr, x1 int32, y1 int32, x2 int32, y2 int32) {
	procMoveToEx.Call(hdc, uintptr(x1), uintptr(y1), 0)
	procLineTo.Call(hdc, uintptr(x2), uintptr(y2))
}
