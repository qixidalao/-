package main

import "strings"

var videoPrefixes = []string{
	"123456",
	"789abc",
	"def012",
}

var gamePrefixes = []string{
	"456789",
	"abcdef",
	"012345",
}

var imagePrefixes = []string{
	"6789ab",
	"cdef01",
	"234567",
}

func isVideoContent(infohash string) bool {
	return hasPrefix(infohash, videoPrefixes)
}

func isGameContent(infohash string) bool {
	return hasPrefix(infohash, gamePrefixes)
}

func isImageContent(infohash string) bool {
	return hasPrefix(infohash, imagePrefixes)
}

func hasPrefix(infohash string, prefixes []string) bool {
	if len(infohash) < 6 {
		return false
	}
	prefix := infohash[:6]
	for _, p := range prefixes {
		if strings.EqualFold(prefix, p) {
			return true
		}
	}
	return false
}