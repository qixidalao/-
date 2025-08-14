package main

import (
	"fmt"
	"math/rand"
	"time"
)

func init() {
    rand.Seed(time.Now().UnixNano())
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	
	switch {
	case h > 0:
		return fmt.Sprintf("%d小时%02d分%02d秒", h, m, s)
	case m > 0:
		return fmt.Sprintf("%d分%02d秒", m, s)
	default:
		return fmt.Sprintf("%d秒", s)
	}
}