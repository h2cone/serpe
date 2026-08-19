//go:build windows

package workdir

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	clsctxInprocServer   = 0x1
	coinitApartment      = 0x2
	coinitDisableOLE1DDE = 0x4
	fosNoChangeDir       = 0x8
	fosPickFolders       = 0x20
	fosForceFileSystem   = 0x40
	fosPathMustExist     = 0x800
	sigdnFileSysPath     = 0x80058000
	rpcEChangedMode      = 0x80010106
	hresultCancel        = 0x800704C7
	hresultAbort         = 0x80004004
	sFalse               = 0x1
)

var (
	ole32                           = windows.NewLazySystemDLL("ole32.dll")
	shell32                         = windows.NewLazySystemDLL("shell32.dll")
	user32                          = windows.NewLazySystemDLL("user32.dll")
	procCoInitializeEx              = ole32.NewProc("CoInitializeEx")
	procCoUninitialize              = ole32.NewProc("CoUninitialize")
	procCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree               = ole32.NewProc("CoTaskMemFree")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	procGetForegroundWindow         = user32.NewProc("GetForegroundWindow")

	clsidFileOpenDialog = windows.GUID{
		Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE,
		Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7},
	}
	iidIFileOpenDialog = windows.GUID{
		Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768,
		Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60},
	}
	iidIShellItem = windows.GUID{
		Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE,
		Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE},
	}
)

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

type iFileOpenDialogVtbl struct {
	iUnknownVtbl
	Show                uintptr
	SetFileTypes        uintptr
	SetFileTypeIndex    uintptr
	GetFileTypeIndex    uintptr
	Advise              uintptr
	Unadvise            uintptr
	SetOptions          uintptr
	GetOptions          uintptr
	SetDefaultFolder    uintptr
	SetFolder           uintptr
	GetFolder           uintptr
	GetCurrentSelection uintptr
	SetFileName         uintptr
	GetFileName         uintptr
	SetTitle            uintptr
	SetOkButtonLabel    uintptr
	SetFileNameLabel    uintptr
	GetResult           uintptr
	AddPlace            uintptr
	SetDefaultExtension uintptr
	Close               uintptr
	SetClientGuid       uintptr
	ClearClientData     uintptr
	SetFilter           uintptr
	GetResults          uintptr
	GetSelectedItems    uintptr
}

type iFileOpenDialog struct{ vtbl *iFileOpenDialogVtbl }

type iShellItemVtbl struct {
	iUnknownVtbl
	BindToHandler  uintptr
	GetParent      uintptr
	GetDisplayName uintptr
	GetAttributes  uintptr
	Compare        uintptr
}

type iShellItem struct{ vtbl *iShellItemVtbl }

func pickNative(ctx context.Context, start string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type outcome struct {
		path string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		path, err := showWindowsFolderDialog(ctx, start)
		done <- outcome{path, err}
	}()
	// Wait for the STA thread so the process-wide picker lock outlives the
	// dialog even when the HTTP request is canceled.
	result := <-done
	if result.err != nil && !errors.Is(result.err, ErrCanceled) &&
		!errors.Is(result.err, context.Canceled) && !errors.Is(result.err, context.DeadlineExceeded) {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, result.err)
	}
	return result.path, result.err
}

func showWindowsFolderDialog(ctx context.Context, start string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	uninit, err := coInitializeSTA()
	if err != nil {
		return "", err
	}
	defer uninit()

	dialog, err := createFileOpenDialog()
	if err != nil {
		return "", err
	}
	defer dialog.release()

	options, err := dialog.getOptions()
	if err != nil {
		return "", err
	}
	options |= fosPickFolders | fosForceFileSystem | fosPathMustExist | fosNoChangeDir
	if err := dialog.setOptions(options); err != nil {
		return "", err
	}
	title, err := windows.UTF16PtrFromString("Choose a working folder")
	if err != nil {
		return "", err
	}
	if err := dialog.setTitle(title); err != nil {
		return "", err
	}
	if start != "" {
		if item, itemErr := shellItemFromPath(start); itemErr == nil && item != nil {
			_ = dialog.setFolder(item)
			item.release()
		}
	}

	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			dialog.close(hresultAbort)
		case <-closed:
		}
	}()
	defer close(closed)

	owner, _, _ := procGetForegroundWindow.Call()
	err = dialog.show(owner)
	if err != nil && owner != 0 && !errors.Is(err, ErrCanceled) && ctx.Err() == nil {
		err = dialog.show(0)
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	item, err := dialog.getResult()
	if err != nil {
		return "", err
	}
	defer item.release()
	return item.displayName(sigdnFileSysPath)
}

func coInitializeSTA() (func(), error) {
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartment|coinitDisableOLE1DDE)
	code := uint32(hr)
	switch code {
	case 0, sFalse:
		return func() { procCoUninitialize.Call() }, nil
	case rpcEChangedMode:
		return func() {}, nil
	default:
		return func() {}, fmt.Errorf("CoInitializeEx HRESULT 0x%08X", code)
	}
}

func createFileOpenDialog() (*iFileOpenDialog, error) {
	var dialog *iFileOpenDialog
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)),
		uintptr(unsafe.Pointer(&dialog)),
	)
	if err := hresultErr(hr); err != nil {
		return nil, err
	}
	if dialog == nil || dialog.vtbl == nil {
		return nil, ErrUnavailable
	}
	return dialog, nil
}

func shellItemFromPath(path string) (*iShellItem, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var item *iShellItem
	hr, _, _ := procSHCreateItemFromParsingName.Call(
		uintptr(unsafe.Pointer(ptr)),
		0,
		uintptr(unsafe.Pointer(&iidIShellItem)),
		uintptr(unsafe.Pointer(&item)),
	)
	if err := hresultErr(hr); err != nil {
		return nil, err
	}
	return item, nil
}

func (d *iFileOpenDialog) release() {
	syscall.SyscallN(d.vtbl.Release, uintptr(unsafe.Pointer(d)))
}

func (d *iFileOpenDialog) show(owner uintptr) error {
	hr, _, _ := syscall.SyscallN(d.vtbl.Show, uintptr(unsafe.Pointer(d)), owner)
	return hresultErr(hr)
}

func (d *iFileOpenDialog) getOptions() (uint32, error) {
	var options uint32
	hr, _, _ := syscall.SyscallN(d.vtbl.GetOptions, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(&options)))
	return options, hresultErr(hr)
}

func (d *iFileOpenDialog) setOptions(options uint32) error {
	hr, _, _ := syscall.SyscallN(d.vtbl.SetOptions, uintptr(unsafe.Pointer(d)), uintptr(options))
	return hresultErr(hr)
}

func (d *iFileOpenDialog) setTitle(title *uint16) error {
	hr, _, _ := syscall.SyscallN(d.vtbl.SetTitle, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(title)))
	return hresultErr(hr)
}

func (d *iFileOpenDialog) setFolder(item *iShellItem) error {
	hr, _, _ := syscall.SyscallN(d.vtbl.SetFolder, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(item)))
	return hresultErr(hr)
}

func (d *iFileOpenDialog) getResult() (*iShellItem, error) {
	var item *iShellItem
	hr, _, _ := syscall.SyscallN(d.vtbl.GetResult, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(&item)))
	if err := hresultErr(hr); err != nil {
		return nil, err
	}
	if item == nil || item.vtbl == nil {
		return nil, ErrUnavailable
	}
	return item, nil
}

func (d *iFileOpenDialog) close(code uintptr) {
	syscall.SyscallN(d.vtbl.Close, uintptr(unsafe.Pointer(d)), code)
}

func (s *iShellItem) release() {
	syscall.SyscallN(s.vtbl.Release, uintptr(unsafe.Pointer(s)))
}

func (s *iShellItem) displayName(sigdn uint32) (string, error) {
	var ptr *uint16
	hr, _, _ := syscall.SyscallN(s.vtbl.GetDisplayName, uintptr(unsafe.Pointer(s)), uintptr(sigdn), uintptr(unsafe.Pointer(&ptr)))
	if err := hresultErr(hr); err != nil {
		return "", err
	}
	if ptr == nil {
		return "", ErrUnavailable
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(ptr)))
	return windows.UTF16PtrToString(ptr), nil
}

func hresultErr(hr uintptr) error {
	code := uint32(hr)
	switch code {
	case 0, sFalse:
		return nil
	case hresultCancel, hresultAbort:
		return ErrCanceled
	default:
		return fmt.Errorf("folder dialog HRESULT 0x%08X", code)
	}
}
