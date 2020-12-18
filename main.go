package main

import (
	"fmt"
	"net/http"
	"regexp"
	"sync"

	"golang.org/x/net/html"
)

const initialResultsSize = 50

// Fetcher returns the body of URL and
// a slice of URLs found on that page.
type Fetcher interface {
	Fetch(url string) (body string, urls []string, err error)
}

// SafeMap is a "thread-safe" string->bool Map
// We'll use it to remember which sites we've already visited
type SafeMap struct {
	sync.Mutex
	v map[string]bool
}

func (safeMap *SafeMap) flip(name string) bool {
	safeMap.Lock()
	defer safeMap.Unlock()
	// Result should be saved
	result := safeMap.v[name]
	// Whatever the value was, turn it to true
	safeMap.v[name] = true
	return result
}

// Crawl uses fetcher to recursively crawl
// pages starting with url, to a maximum of depth.
func Crawl(url string, depth int, fetcher Fetcher, parentChan chan bool, urlMap *SafeMap) {
	// Once we're done we inform our parent
	defer func() {
		// fmt.Println(url, "Done")
		parentChan <- true
	}()

	if depth <= 0 {
		return
	}

	// First we check if this url has already been visited
	if urlMap.flip(url) {
		return
	}
	body, urls, err := fetcher.Fetch(url)

	if err != nil {
		// If we can't find the url, return (future iterations)
		fmt.Println(err)
		return
	}

	fmt.Printf("Crawling: %s %q, child length: %d\n", url, body, len(urls))

	doneCh := make(chan bool, len(urls))
	numToExplore := len(urls)

	for _, u := range urls {
		go Crawl(u, depth-1, fetcher, doneCh, urlMap)
	}

	numFin := 0
	for {
		if numFin >= numToExplore {
			break
		}
		<-doneCh
		numFin++

	}
	return
}

func main() {
	var actualFetcher = realFetcher{}

	doneCh := make(chan bool)
	urlMap := SafeMap{v: make(map[string]bool)}
	go Crawl("https://xkcd.com/", 3, actualFetcher, doneCh, &urlMap)
	<-doneCh

}

// fakeFetcher is Fetcher that returns canned results.
type fakeFetcher map[string]*fakeResult

// realFetcher is real Fetcher that returns real results.
type realFetcher struct{}

func (f realFetcher) Fetch(url string) (string, []string, error) {
	results := make([]string, 0, initialResultsSize)

	resp, err := http.Get(url)
	if err != nil {
		fmt.Println(err)
		return "", nil, err
	}

	z := html.NewTokenizer(resp.Body)

	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			// fmt.Println(url, " Finished fetch")
			return "", results, nil
		case html.StartTagToken, html.EndTagToken:
			tn, _ := z.TagName()
			if len(tn) == 1 && tn[0] == 'a' {
				if tt == html.StartTagToken {
					for key, val, moreAttrs := z.TagAttr(); ; _, val, moreAttrs = z.TagAttr() {
						if string(key) == "href" {
							if isHTTP, _ := regexp.Match(`https?://.*`, val); isHTTP {
								results = append(results, string(val))
							}
							break
						}
						if !moreAttrs {
							break
						}

					}

				}
			}
		}
	}
}

type fakeResult struct {
	body string
	urls []string
}

func (f fakeFetcher) Fetch(url string) (string, []string, error) {
	if res, ok := f[url]; ok {
		return res.body, res.urls, nil
	}
	return "", nil, fmt.Errorf("not found: %s", url)

}

// fetcher is a populated fakeFetcher.
var fetcher = fakeFetcher{
	"https://golang.org/": &fakeResult{
		"The Go Programming Language",
		[]string{
			"https://golang.org/pkg/",
			"https://golang.org/cmd/",
		},
	},
	"https://golang.org/pkg/": &fakeResult{
		"Packages",
		[]string{
			"https://golang.org/",
			"https://golang.org/cmd/",
			"https://golang.org/pkg/fmt/",
			"https://golang.org/pkg/os/",
		},
	},
	"https://golang.org/pkg/fmt/": &fakeResult{
		"Package fmt",
		[]string{
			"https://golang.org/",
			"https://golang.org/pkg/",
		},
	},
	"https://golang.org/pkg/os/": &fakeResult{
		"Package os",
		[]string{
			"https://golang.org/",
			"https://golang.org/pkg/",
		},
	},
}
