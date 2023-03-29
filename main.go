package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sync/semaphore"
)

var (
	bearer    = "Bearer " + os.Getenv("RANCHER_TOKEN")
	serverURL = os.Getenv("RANCHER_SERVER")
	sem       = semaphore.NewWeighted(100)
)

func main() {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		log.Fatalf("Failed to parse server URL: %s", err)
	}

	hostName := []byte(parsed.Hostname())
	seen := map[string]bool{serverURL: true}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   time.Second * 60,
	}
	var wg sync.WaitGroup
	newURLs := make(chan *url.URL)
	done := make(chan struct{})
	wg.Add(1)
	go GetUrls(client, serverURL, hostName, &wg, newURLs)
	go func() {
		wg.Wait()
		close(done)
	}()
	actions := map[string]bool{}
	func() {
		for {
			select {
			case <-done:
				return
			case parsed = <-newURLs:
				action := parsed.Query().Get("action")
				if action != "" {
					actions[parsed.String()] = true
				}
				parsed.RawQuery = ""
				link := parsed.String()
				if seen[link] {
					continue
				}
				seen[link] = true
				wg.Add(1)
				go GetUrls(client, link, hostName, &wg, newURLs)
			}
		}
	}()

	actionList := make([]string, 0, len(actions))
	for act := range actions {
		actionList = append(actionList, act)
	}
	sort.Strings(actionList)
	fmt.Println("ACTIONS:")
	fmt.Println(strings.Join(actionList, "\n"))
}

func GetUrls(client *http.Client, nextUrl string, hostname []byte, wg *sync.WaitGroup, newURLs chan *url.URL) {
	defer wg.Done()
	if strings.HasSuffix(nextUrl, "readme") {
		return
	}
	if err := sem.Acquire(context.TODO(), 1); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to acquire semaphore: %s\n", err)
		return
	}
	fmt.Println("visiting:", nextUrl)
	defer sem.Release(1)
	req, err := http.NewRequest(http.MethodGet, nextUrl, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create request: %s\n", err)
		return
	}
	req.Header.Set("Authorization", bearer)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed request: %s\n", err)
		return
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(ScanURL(hostname))
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024*1024)
	for scanner.Scan() {
		link := scanner.Text()
		if !strings.HasPrefix(link, "https://") {
			link = "https://" + link
		}
		parsed, err := url.Parse(link)
		if err != nil {
			continue
		}
		newURLs <- parsed

	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed reading input: %s\n", err)
	}
}
func add(s string, m map[string]bool) map[string]bool {
	if m == nil {
		m = make(map[string]bool)
	}
	m[s] = true
	return m
}

func ScanURL(hostname []byte) bufio.SplitFunc {
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}
		if start := bytes.Index(data, hostname); start >= 0 {

			for width, i := 0, start; i < len(data); i += width {
				var r rune
				r, width = utf8.DecodeRune(data[i:])
				if isEnd(r) {
					return i + width, data[start:i], nil
				}
			}

		}
		// Request more data.
		return 0, nil, nil
	}
}

func isEnd(r rune) bool {
	return unicode.IsSpace(r) || r == '"'

}
