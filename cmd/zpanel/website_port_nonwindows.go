//go:build !windows && !linux

package main

func websiteHTTPPort() int {
	return 8081
}
