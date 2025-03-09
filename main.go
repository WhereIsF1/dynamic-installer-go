package main

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
)

const (
	className   = "DynamicInstallerClass"
	windowTitle = "Izumis Dynamic Installer"
)

const (
	PBM_SETRANGE                      = 0x0401
	PBM_SETPOS                        = 0x0402
	PBM_SETBARCOLOR                   = 0x409
	PBM_SETBKCOLOR                    = 0x2001
	CLR_DEFAULT                       = 0xFFFFFFFF
	BS_FLAT                           = 0x8000
	DWMWA_USE_IMMERSIVE_DARK_MODE     = 20
	WINHTTP_ACCESS_TYPE_DEFAULT_PROXY = 0
	WINHTTP_NO_PROXY_NAME             = 0
	WINHTTP_NO_PROXY_BYPASS           = 0
	WINHTTP_FLAG_SECURE               = 0x00800000
	WINHTTP_ADDREQ_FLAG_ADD           = 0x20000000
)

const (
	BTN_INSTALL   = 1
	BTN_CANCEL    = 2
	STATIC_STATUS = 3
	PROGRESS_BAR  = 4
	CHK_ROSSA     = 5
	CHK_SYNCER    = 6
)

const (
	COLOR_BG_DARK   = 0x2D2D30
	COLOR_BG_MEDIUM = 0x3E3E42
	COLOR_TEXT      = 0xFFFFFF
	COLOR_ACCENT    = 0x007ACC
)

var (
	hInstance     win.HINSTANCE
	hwndMain      win.HWND
	hwndStatus    win.HWND
	hwndProgress  win.HWND
	hwndRossaChk  win.HWND
	hwndSyncerChk win.HWND
	hBrush        win.HBRUSH
	hFont         win.HFONT
	isInstalling  bool
	installRossa  bool
	installSyncer bool
)

var (
	gdi32                         = syscall.NewLazyDLL("gdi32.dll")
	procCreateSolidBrush          = gdi32.NewProc("CreateSolidBrush")
	user32                        = syscall.NewLazyDLL("user32.dll")
	procSetWindowTextW            = user32.NewProc("SetWindowTextW")
	dwmapi                        = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute     = dwmapi.NewProc("DwmSetWindowAttribute")
	winhttp                       = syscall.NewLazyDLL("winhttp.dll")
	procWinHttpOpen               = winhttp.NewProc("WinHttpOpen")
	procWinHttpConnect            = winhttp.NewProc("WinHttpConnect")
	procWinHttpOpenRequest        = winhttp.NewProc("WinHttpOpenRequest")
	procWinHttpSendRequest        = winhttp.NewProc("WinHttpSendRequest")
	procWinHttpReceiveResponse    = winhttp.NewProc("WinHttpReceiveResponse")
	procWinHttpQueryDataAvailable = winhttp.NewProc("WinHttpQueryDataAvailable")
	procWinHttpReadData           = winhttp.NewProc("WinHttpReadData")
	procWinHttpCloseHandle        = winhttp.NewProc("WinHttpCloseHandle")
)

type AddonInstaller struct {
	Name       string
	URL        string
	TargetPath string
}

type ParsedURL struct {
	Scheme   string
	Host     string
	Port     int
	Path     string
	RawQuery string
}

func CreateSolidBrush(color int) win.HBRUSH {
	ret, _, _ := procCreateSolidBrush.Call(uintptr(color))
	return win.HBRUSH(ret)
}

func setDarkTitleBar(hwnd win.HWND) {
	darkMode := 1
	procDwmSetWindowAttribute.Call(
		uintptr(hwnd),
		uintptr(DWMWA_USE_IMMERSIVE_DARK_MODE),
		uintptr(unsafe.Pointer(&darkMode)),
		uintptr(4),
	)
}

func SetWindowText(hwnd win.HWND, text *uint16) bool {
	ret, _, _ := procSetWindowTextW.Call(
		uintptr(hwnd),
		uintptr(unsafe.Pointer(text)),
	)
	return ret != 0
}

func MAKELPARAM(lo, hi uint16) uintptr {
	return uintptr(uint32(hi)<<16 | uint32(lo))
}

func parseURL(rawURL string) (ParsedURL, error) {
	result := ParsedURL{}

	if strings.HasPrefix(rawURL, "https://") {
		result.Scheme = "https"
		rawURL = strings.TrimPrefix(rawURL, "https://")
	} else if strings.HasPrefix(rawURL, "http://") {
		result.Scheme = "http"
		rawURL = strings.TrimPrefix(rawURL, "http://")
	} else {
		return result, fmt.Errorf("unknown scheme in URL: %s", rawURL)
	}

	hostAndPath := strings.SplitN(rawURL, "/", 2)
	hostPart := hostAndPath[0]

	hostAndPort := strings.SplitN(hostPart, ":", 2)
	result.Host = hostAndPort[0]

	if result.Scheme == "https" {
		result.Port = 443
	} else {
		result.Port = 80
	}

	if len(hostAndPort) > 1 {
		port, err := strconv.Atoi(hostAndPort[1])
		if err != nil {
			return result, fmt.Errorf("invalid port: %s", hostAndPort[1])
		}
		result.Port = port
	}

	if len(hostAndPath) > 1 {
		pathAndQuery := strings.SplitN(hostAndPath[1], "?", 2)
		result.Path = "/" + pathAndQuery[0]

		if len(pathAndQuery) > 1 {
			result.RawQuery = "?" + pathAndQuery[1]
		}
	} else {
		result.Path = "/"
	}

	return result, nil
}

func downloadFile(url, dest string) error {
	// Parse URL
	parsedURL, err := parseURL(url)
	if err != nil {
		return err
	}

	// Create output file
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()

	// Initialize WinHTTP
	userAgent := syscall.StringToUTF16Ptr("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	hSession, _, _ := procWinHttpOpen.Call(
		uintptr(unsafe.Pointer(userAgent)),
		uintptr(WINHTTP_ACCESS_TYPE_DEFAULT_PROXY),
		uintptr(WINHTTP_NO_PROXY_NAME),
		uintptr(WINHTTP_NO_PROXY_BYPASS),
		0)

	if hSession == 0 {
		return fmt.Errorf("WinHttpOpen failed")
	}
	defer procWinHttpCloseHandle.Call(hSession)

	// Connect to server
	serverName := syscall.StringToUTF16Ptr(parsedURL.Host)
	hConnect, _, _ := procWinHttpConnect.Call(
		hSession,
		uintptr(unsafe.Pointer(serverName)),
		uintptr(parsedURL.Port),
		0)

	if hConnect == 0 {
		return fmt.Errorf("WinHttpConnect failed")
	}
	defer procWinHttpCloseHandle.Call(hConnect)

	// Create request
	pathWithQuery := parsedURL.Path
	if parsedURL.RawQuery != "" {
		pathWithQuery += parsedURL.RawQuery
	}
	path := syscall.StringToUTF16Ptr(pathWithQuery)
	verb := syscall.StringToUTF16Ptr("GET")
	var flags uintptr = 0
	if parsedURL.Scheme == "https" {
		flags = WINHTTP_FLAG_SECURE
	}

	hRequest, _, _ := procWinHttpOpenRequest.Call(
		hConnect,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(path)),
		0,
		0,
		0,
		flags)

	if hRequest == 0 {
		return fmt.Errorf("WinHttpOpenRequest failed")
	}
	defer procWinHttpCloseHandle.Call(hRequest)

	// Send request
	_, _, _ = procWinHttpSendRequest.Call(
		hRequest,
		0,
		0,
		0,
		0,
		0,
		0)

	// Wait for response
	_, _, _ = procWinHttpReceiveResponse.Call(
		hRequest,
		0)

	// Read data
	var bytesAvailable uint32
	buffer := make([]byte, 8192)

	for {
		// Check how many bytes are available
		ret, _, _ := procWinHttpQueryDataAvailable.Call(
			hRequest,
			uintptr(unsafe.Pointer(&bytesAvailable)))

		if ret == 0 || bytesAvailable == 0 {
			break
		}

		// Cap buffer size to bytes available
		toRead := uint32(len(buffer))
		if bytesAvailable < toRead {
			toRead = bytesAvailable
		}

		var bytesRead uint32
		ret, _, _ = procWinHttpReadData.Call(
			hRequest,
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(toRead),
			uintptr(unsafe.Pointer(&bytesRead)))

		if ret == 0 || bytesRead == 0 {
			break
		}

		// Write to file
		_, err = file.Write(buffer[:bytesRead])
		if err != nil {
			return err
		}

		// Add small delay to make download patterns less suspicious
		time.Sleep(5 * time.Millisecond)
	}

	return nil
}

func (a *AddonInstaller) InstallAddon() error {
	tempZipPath := filepath.Join(os.TempDir(), a.Name+".zip")

	err := downloadFile(a.URL, tempZipPath)
	if err != nil {
		return fmt.Errorf("error downloading addon: %v", err)
	}

	err = extractZip(tempZipPath, a.TargetPath)
	if err != nil {
		return fmt.Errorf("error extracting addon: %v", err)
	}

	os.Remove(tempZipPath)

	return nil
}

func extractZip(zipPath, destPath string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	if err := os.MkdirAll(destPath, os.ModePerm); err != nil {
		return err
	}

	for _, file := range reader.File {
		filePath := filepath.Join(destPath, file.Name)

		if !strings.HasPrefix(filePath, filepath.Clean(destPath)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", filePath)
		}

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(filePath, os.ModePerm); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(filePath), os.ModePerm); err != nil {
			return err
		}

		destFile, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return err
		}

		srcFile, err := file.Open()
		if err != nil {
			destFile.Close()
			return err
		}

		if _, err := io.Copy(destFile, srcFile); err != nil {
			destFile.Close()
			srcFile.Close()
			return err
		}

		destFile.Close()
		srcFile.Close()
	}

	return nil
}

func init() {
	runtime.LockOSThread()
}

func main() {
	f, _ := os.Create("installer_log.txt")
	if f != nil {
		defer f.Close()
		log.SetOutput(f)
	}

	log.Println("Starting installer")

	hInstance = win.GetModuleHandle(nil)
	if hInstance == 0 {
		log.Fatal("GetModuleHandle failed")
		os.Exit(1)
	}

	var initCtrls win.INITCOMMONCONTROLSEX
	initCtrls.DwSize = uint32(unsafe.Sizeof(initCtrls))
	initCtrls.DwICC = win.ICC_PROGRESS_CLASS
	win.InitCommonControlsEx(&initCtrls)

	hBrush = CreateSolidBrush(COLOR_BG_DARK)

	hFont = win.HFONT(win.GetStockObject(win.DEFAULT_GUI_FONT))

	registerWindowClass()

	createMainWindow()

	win.ShowWindow(hwndMain, win.SW_SHOWNORMAL)
	win.UpdateWindow(hwndMain)

	var msg win.MSG
	for {
		if ret := win.GetMessage(&msg, 0, 0, 0); ret == 0 {
			break
		} else if ret == -1 {
			log.Println("GetMessage failed")
			break
		}

		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}

	win.DeleteObject(win.HGDIOBJ(hBrush))
}

func registerWindowClass() {
	var wcex win.WNDCLASSEX
	wcex.CbSize = uint32(unsafe.Sizeof(wcex))
	wcex.Style = win.CS_HREDRAW | win.CS_VREDRAW
	wcex.LpfnWndProc = syscall.NewCallback(wndProc)
	wcex.HInstance = hInstance
	wcex.HCursor = win.LoadCursor(0, syscall.StringToUTF16Ptr("IDC_ARROW"))
	wcex.HbrBackground = hBrush
	wcex.LpszClassName = syscall.StringToUTF16Ptr(className)

	if atom := win.RegisterClassEx(&wcex); atom == 0 {
		log.Fatal("RegisterClassEx failed")
		os.Exit(1)
	}
}

func createMainWindow() {
	hwndMain = win.CreateWindowEx(
		0,
		syscall.StringToUTF16Ptr(className),
		syscall.StringToUTF16Ptr(windowTitle),
		win.WS_OVERLAPPED|win.WS_CAPTION|win.WS_SYSMENU|win.WS_MINIMIZEBOX,
		win.CW_USEDEFAULT, win.CW_USEDEFAULT,
		400, 245,
		0, 0, hInstance, nil)

	if hwndMain == 0 {
		log.Fatal("CreateWindowEx failed")
		os.Exit(1)
	}

	setDarkTitleBar(hwndMain)

	hwndTitle := win.CreateWindowEx(
		0,
		syscall.StringToUTF16Ptr("STATIC"),
		syscall.StringToUTF16Ptr("IZUMIS DYNAMIC INSTALLER"),
		win.WS_VISIBLE|win.WS_CHILD|win.SS_CENTER,
		0, 10, 400, 25,
		hwndMain, 0, hInstance, nil)

	win.SendMessage(hwndTitle, win.WM_SETFONT, uintptr(hFont), 1)

	hwndStatus = win.CreateWindowEx(
		0,
		syscall.StringToUTF16Ptr("STATIC"),
		syscall.StringToUTF16Ptr("Ready to install"),
		win.WS_VISIBLE|win.WS_CHILD|win.SS_CENTER,
		20, 45, 360, 20,
		hwndMain, win.HMENU(STATIC_STATUS), hInstance, nil)

	win.SendMessage(hwndStatus, win.WM_SETFONT, uintptr(hFont), 1)

	hwndProgress = win.CreateWindowEx(
		0,
		syscall.StringToUTF16Ptr("msctls_progress32"),
		nil,
		win.WS_VISIBLE|win.WS_CHILD,
		20, 75, 360, 20,
		hwndMain, win.HMENU(PROGRESS_BAR), hInstance, nil)

	win.SendMessage(hwndProgress, PBM_SETRANGE, 0, MAKELPARAM(0, 100))

	hwndRossaChk = win.CreateWindowEx(
		0,
		syscall.StringToUTF16Ptr("BUTTON"),
		syscall.StringToUTF16Ptr("Install Rossa"),
		win.WS_VISIBLE|win.WS_CHILD|win.BS_AUTOCHECKBOX,
		20, 105, 360, 20,
		hwndMain, win.HMENU(CHK_ROSSA), hInstance, nil)

	win.SendMessage(hwndRossaChk, win.BM_SETCHECK, 1, 0)
	win.SendMessage(hwndRossaChk, win.WM_SETFONT, uintptr(hFont), 1)

	hwndSyncerChk = win.CreateWindowEx(
		0,
		syscall.StringToUTF16Ptr("BUTTON"),
		syscall.StringToUTF16Ptr("Install Izumis Dynamic Syncer"),
		win.WS_VISIBLE|win.WS_CHILD|win.BS_AUTOCHECKBOX,
		20, 130, 360, 20,
		hwndMain, win.HMENU(CHK_SYNCER), hInstance, nil)

	win.SendMessage(hwndSyncerChk, win.BM_SETCHECK, 1, 0)
	win.SendMessage(hwndSyncerChk, win.WM_SETFONT, uintptr(hFont), 1)

	hwndInstall := win.CreateWindowEx(
		0,
		syscall.StringToUTF16Ptr("BUTTON"),
		syscall.StringToUTF16Ptr("INSTALL"),
		win.WS_VISIBLE|win.WS_CHILD|win.BS_PUSHBUTTON,
		100, 160, 90, 30,
		hwndMain, win.HMENU(BTN_INSTALL), hInstance, nil)

	win.SendMessage(hwndInstall, win.WM_SETFONT, uintptr(hFont), 1)

	hwndExit := win.CreateWindowEx(
		0,
		syscall.StringToUTF16Ptr("BUTTON"),
		syscall.StringToUTF16Ptr("EXIT"),
		win.WS_VISIBLE|win.WS_CHILD|win.BS_PUSHBUTTON,
		210, 160, 90, 30,
		hwndMain, win.HMENU(BTN_CANCEL), hInstance, nil)

	win.SendMessage(hwndExit, win.WM_SETFONT, uintptr(hFont), 1)

	centerWindow(hwndMain)
}

func centerWindow(hwnd win.HWND) {
	screenWidth := int32(win.GetSystemMetrics(win.SM_CXSCREEN))
	screenHeight := int32(win.GetSystemMetrics(win.SM_CYSCREEN))

	var rect win.RECT
	win.GetWindowRect(hwnd, &rect)
	width := rect.Right - rect.Left
	height := rect.Bottom - rect.Top

	x := (screenWidth - width) / 2
	y := (screenHeight - height) / 2

	win.SetWindowPos(hwnd, 0, x, y, width, height, win.SWP_NOZORDER)
}

func setStatusText(text string) {
	win.SendMessage(hwndStatus, win.WM_SETTEXT, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(text))))
}

func setProgressValue(value int) {
	win.SendMessage(hwndProgress, PBM_SETPOS, uintptr(value), 0)
}

func startInstallation() {
	if isInstalling {
		return
	}

	isInstalling = true

	installRossa = win.SendMessage(hwndRossaChk, win.BM_GETCHECK, 0, 0) == win.BST_CHECKED
	installSyncer = win.SendMessage(hwndSyncerChk, win.BM_GETCHECK, 0, 0) == win.BST_CHECKED

	win.EnableWindow(win.GetDlgItem(hwndMain, BTN_INSTALL), false)

	go func() {
		var err error
		defer func() {
			win.SendMessage(hwndMain, win.WM_APP, 0, 0)
		}()

		win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Creating dynamic folder..."))))

		dir, _ := os.Getwd()
		dynamicDir := filepath.Join(dir, "dynamic")
		err = os.MkdirAll(dynamicDir, os.ModePerm)
		if err != nil {
			win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Error creating folder: "+err.Error()))))
			return
		}

		serialText := "--AR_SERIAL--"
		configContent := fmt.Sprintf(`{
    "serials": ["%s"],
    "startup_rune_scripts": ["com:scphook", "com:Arsenic", "com:WinAPI Stub"]
}`, serialText)

		configPath := filepath.Join(dynamicDir, "config.jsonc")
		err = os.WriteFile(configPath, []byte(configContent), 0644)
		if err != nil {
			win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Error creating config: "+err.Error()))))
			return
		}

		files := []struct {
			URL  string
			Name string
		}{
			{
				URL:  "https://cdn.discordapp.com/attachments/1340425136737615942/1346838472379334676/dynamic.dll?ex=67ceea92&is=67cd9912&hm=621fc6d200bce4a9a041d9fd2d06f78c87ff48198f5001f55f7d38e877f128c9&",
				Name: "dynamic.dll",
			},
			{
				URL:  "https://cdn.discordapp.com/attachments/1340425659998146682/1340428290409889874/dynamic_loader.exe?ex=67ceaae0&is=67cd5960&hm=f8ba6db5f9393c75cf44b9d3dcb78aa8cfdfc9c24f11b3f8b4130e9c54f75b83&",
				Name: "dynamic_loader.exe",
			},
		}

		totalSteps := len(files)
		if installRossa {
			totalSteps++
		}
		if installSyncer {
			totalSteps++
		}

		for i, file := range files {
			progress := (i * 100) / totalSteps
			win.SendMessage(hwndMain, win.WM_APP+2, uintptr(progress), 0)

			statusText := fmt.Sprintf("Downloading %s (%d/%d)...", file.Name, i+1, len(files))
			win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(statusText))))

			destPath := filepath.Join(dynamicDir, file.Name)
			err = downloadFile(file.URL, destPath)
			if err != nil {
				win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Error: "+err.Error()))))
				return
			}
		}

		currentStep := len(files)

		if installRossa {
			progress := (currentStep * 100) / totalSteps
			win.SendMessage(hwndMain, win.WM_APP+2, uintptr(progress), 0)
			win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Installing Rossa addon..."))))

			rossaAddon := &AddonInstaller{
				Name:       "Rossa",
				URL:        "https://cdn.discordapp.com/attachments/1340594754601357366/1340918781668491317/RossaFiles.zip?ex=67cf222e&is=67cdd0ae&hm=fbe21ee4683ad84a36700bd8a3fb0a26e0e85cb3e227cfef6ad83cd1fbd04ded&",
				TargetPath: dynamicDir,
			}

			err = rossaAddon.InstallAddon()
			if err != nil {
				win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Error installing Rossa: "+err.Error()))))
				return
			}

			currentStep++
		}

		if installSyncer {
			progress := (currentStep * 100) / totalSteps
			win.SendMessage(hwndMain, win.WM_APP+2, uintptr(progress), 0)
			win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Installing Izumis Dynamic Syncer..."))))

			syncerURL := "https://github.com/WhereIsF1/dynamic-syncer-go/releases/download/1.0.0/izumis_dynamic_syncer.exe"
			syncerPath := filepath.Join(dynamicDir, "izumis_dynamic_syncer.exe")

			err = downloadFile(syncerURL, syncerPath)
			if err != nil {
				win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Error installing Dynamic Syncer: "+err.Error()))))
				return
			}
		}

		win.SendMessage(hwndMain, win.WM_APP+2, 100, 0)
		win.SendMessage(hwndMain, win.WM_APP+1, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("Installation completed successfully!"))))
		win.SendMessage(hwndMain, win.WM_APP+3, 0, 0)
	}()
}

func wndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case win.WM_COMMAND:
		id := win.LOWORD(uint32(wParam))

		switch id {
		case BTN_INSTALL:
			startInstallation()

		case BTN_CANCEL:
			win.DestroyWindow(hwnd)

		case CHK_ROSSA:
			if win.HIWORD(uint32(wParam)) == win.BN_CLICKED {
			}

		case CHK_SYNCER:
			if win.HIWORD(uint32(wParam)) == win.BN_CLICKED {
			}
		}

	case win.WM_CTLCOLORSTATIC:
		win.SetTextColor(win.HDC(wParam), COLOR_TEXT)
		win.SetBkColor(win.HDC(wParam), COLOR_BG_DARK)
		return uintptr(hBrush)

	case win.WM_CTLCOLORBTN:
		win.SetTextColor(win.HDC(wParam), COLOR_TEXT)
		win.SetBkColor(win.HDC(wParam), COLOR_BG_DARK)
		return uintptr(hBrush)

	case win.WM_APP:
		win.EnableWindow(win.GetDlgItem(hwndMain, BTN_INSTALL), true)
		isInstalling = false

	case win.WM_APP + 1:
		if lParam != 0 {
			SetWindowText(hwndStatus, (*uint16)(unsafe.Pointer(lParam)))
		}

	case win.WM_APP + 2:
		setProgressValue(int(wParam))

	case win.WM_APP + 3:
		win.MessageBox(hwndMain,
			syscall.StringToUTF16Ptr("Installation completed successfully!"),
			syscall.StringToUTF16Ptr("Installation Complete"),
			win.MB_OK|win.MB_ICONINFORMATION)

	case win.WM_CLOSE:
		win.DestroyWindow(hwnd)

	case win.WM_DESTROY:
		win.PostQuitMessage(0)

	default:
		return win.DefWindowProc(hwnd, msg, wParam, lParam)
	}

	return 0
}
