//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32            = syscall.NewLazyDLL("kernel32.dll")
	procBeginUpdateResW    = modkernel32.NewProc("BeginUpdateResourceW")
	procUpdateResourceW    = modkernel32.NewProc("UpdateResourceW")
	procEndUpdateResourceW = modkernel32.NewProc("EndUpdateResourceW")
)

type iconEntry struct {
	Width      byte
	Height     byte
	ColorCount byte
	Reserved   byte
	Planes     uint16
	BitCount   uint16
	Image      []byte
}

func main() {
	exePath := flag.String("exe", "", "path to target exe")
	iconPath := flag.String("icon", "", "path to source ico file")
	flag.Parse()

	if *exePath == "" {
		fmt.Fprintln(os.Stderr, "missing -exe")
		os.Exit(1)
	}
	if *iconPath == "" {
		fmt.Fprintln(os.Stderr, "missing -icon")
		os.Exit(1)
	}

	entries, err := loadIconEntries(*iconPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := updateExeIcon(*exePath, entries); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("Embedded icon into %s\n", *exePath)
}

type icoHeader struct {
	Reserved uint16
	Type     uint16
	Count    uint16
}

type icoDirEntry struct {
	Width       byte
	Height      byte
	ColorCount  byte
	Reserved    byte
	Planes      uint16
	BitCount    uint16
	BytesInRes  uint32
	ImageOffset uint32
}

func loadIconEntries(iconPath string) ([]iconEntry, error) {
	rawICO, err := os.ReadFile(iconPath)
	if err != nil {
		return nil, err
	}
	if len(rawICO) < 6 {
		return nil, fmt.Errorf("invalid ico file: too small")
	}

	var header icoHeader
	if err := binary.Read(bytes.NewReader(rawICO[:6]), binary.LittleEndian, &header); err != nil {
		return nil, err
	}
	if header.Reserved != 0 || header.Type != 1 || header.Count == 0 {
		return nil, fmt.Errorf("invalid ico file: unexpected header")
	}

	entryTableSize := int(header.Count) * 16
	if len(rawICO) < 6+entryTableSize {
		return nil, fmt.Errorf("invalid ico file: truncated directory")
	}

	entries := make([]iconEntry, 0, header.Count)
	dirReader := bytes.NewReader(rawICO[6 : 6+entryTableSize])
	for i := 0; i < int(header.Count); i++ {
		var dir icoDirEntry
		if err := binary.Read(dirReader, binary.LittleEndian, &dir); err != nil {
			return nil, err
		}

		start := int(dir.ImageOffset)
		end := start + int(dir.BytesInRes)
		if start < 0 || end < start || end > len(rawICO) {
			return nil, fmt.Errorf("invalid ico file: image %d out of bounds", i)
		}

		entries = append(entries, iconEntry{
			Width:      dir.Width,
			Height:     dir.Height,
			ColorCount: dir.ColorCount,
			Reserved:   dir.Reserved,
			Planes:     dir.Planes,
			BitCount:   dir.BitCount,
			Image:      append([]byte(nil), rawICO[start:end]...),
		})
	}

	return entries, nil
}

func updateExeIcon(exePath string, entries []iconEntry) error {
	name, err := syscall.UTF16PtrFromString(exePath)
	if err != nil {
		return err
	}
	handle, _, err := procBeginUpdateResW.Call(uintptr(unsafe.Pointer(name)), 0)
	if handle == 0 {
		return err
	}

	discard := true
	defer func() {
		procEndUpdateResourceW.Call(handle, boolToUintptr(discard))
	}()

	for index, entry := range entries {
		r1, _, err := procUpdateResourceW.Call(
			handle,
			uintptr(3),
			uintptr(index+1),
			uintptr(0),
			uintptr(unsafe.Pointer(&entry.Image[0])),
			uintptr(uint32(len(entry.Image))),
		)
		if r1 == 0 {
			return err
		}
	}

	groupData := buildGroupIconResource(entries)
	r1, _, err := procUpdateResourceW.Call(
		handle,
		uintptr(14),
		uintptr(1),
		uintptr(0),
		uintptr(unsafe.Pointer(&groupData[0])),
		uintptr(uint32(len(groupData))),
	)
	if r1 == 0 {
		return err
	}

	discard = false
	return nil
}

func buildGroupIconResource(entries []iconEntry) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(entries)))
	for index, entry := range entries {
		buf.WriteByte(entry.Width)
		buf.WriteByte(entry.Height)
		buf.WriteByte(entry.ColorCount)
		buf.WriteByte(entry.Reserved)
		_ = binary.Write(&buf, binary.LittleEndian, entry.Planes)
		_ = binary.Write(&buf, binary.LittleEndian, entry.BitCount)
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(entry.Image)))
		_ = binary.Write(&buf, binary.LittleEndian, uint16(index+1))
	}
	return buf.Bytes()
}

func boolToUintptr(v bool) uintptr {
	if v {
		return 1
	}
	return 0
}
