package utils

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"runtime"
	"time"
)

func GenerateTimestampID() uint32 {
	// Use milliseconds for longer wrap-around (≈49 days)
	timestamp := uint32(time.Now().UnixNano() / 1e6)

	var randBytes [2]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		panic("failed to generate random bytes: " + err.Error())
	}
	randomPart := binary.BigEndian.Uint16(randBytes[:])

	// Combine: upper 16 bits = timestamp, lower 16 bits = random
	return (timestamp << 16) | uint32(randomPart)
}

func PrintApiLog(parAPI string) {
	m := runtime.MemStats{}
	runtime.ReadMemStats(&m)
	fmt.Printf("[%v] API: %v, GoR#: %v, Memory{Alloc: %v, TotalAlloc: %v, Sys: %v, NumGC: %v}\n", time.Now().UTC().Format("2006-01-02 15:04:05.999999"), parAPI, runtime.NumGoroutine(), FormatBytesCount(m.Alloc), FormatBytesCount(m.TotalAlloc), FormatBytesCount(m.Sys), m.NumGC)
}

func FormatBytesCount(parBytesAmount uint64) string {
	var pvUnit uint64 = 1024

	if parBytesAmount < pvUnit {
		return fmt.Sprintf("%d B", parBytesAmount)
	}

	pvDiv := pvUnit
	pvExp := 0

	for n := parBytesAmount / pvUnit; n >= pvUnit; n /= pvUnit {
		pvDiv *= pvUnit
		pvExp++
	}
	return fmt.Sprintf("%.1f %ciB",
		float64(parBytesAmount)/float64(pvDiv), "KMGTPE"[pvExp])
}
