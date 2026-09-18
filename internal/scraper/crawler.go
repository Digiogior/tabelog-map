package scraper

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"tabelog-map/models"
	"time"

	"github.com/gocolly/colly"
)

// ErrRateLimited is returned by FetchRstInfo when the server responds with HTTP 429.
var ErrRateLimited = errors.New("rate limited (HTTP 429)")

func newCollector(delay time.Duration) *colly.Collector {
	c := colly.NewCollector(colly.AllowedDomains("tabelog.com"))
	c.Limit(&colly.LimitRule{DomainGlob: "*", Delay: delay, Parallelism: 1})
	c.OnRequest(func(r *colly.Request) { r.Headers.Set("User-Agent", "MyResearchBot/1.0") })
	return c
}

func FetchRstInfo(url string, city string) (models.Restaurant, error) {
	var rst models.Restaurant
	rst.City = city
	rst.URL = url
	var rateLimited bool

	c1 := newCollector(5 * time.Second)
	c1.OnError(func(r *colly.Response, err error) {
		if r.StatusCode == 429 {
			rateLimited = true
		}
	})

	c1.OnHTML(".rdheader-rstname h2.display-name", func(e *colly.HTMLElement) {
		rst.Name = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rdheader-rstname .alias", func(e *colly.HTMLElement) {
		rst.Alias = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rdheader-rstname .pillow-word", func(e *colly.HTMLElement) {
		rst.PillowWord = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rstinfo-table__address", func(e *colly.HTMLElement) {
		rst.Address = strings.TrimSpace(e.Text)
	})
	c1.OnHTML(".rdheader-rating__score-val-dtl", func(e *colly.HTMLElement) {
		text := strings.TrimSpace(e.Text)
		if text == "-" || text == "" {
			return
		}
		var err error
		rst.Rating, err = strconv.ParseFloat(text, 64)
		if err != nil {
			fmt.Println("Error converting rating to float64:", err)
		}
	})
	c1.OnHTML(".rdheader-budget", func(e *colly.HTMLElement) {
		e.ForEach(".rdheader-budget__price-target", func(i int, e1 *colly.HTMLElement) {
			separator := "-"

			cleanAndParse := func(s string) int {
				s = strings.TrimPrefix(s, "JPY ")
				s = strings.ReplaceAll(s, ",", "")
				val, _ := strconv.Atoi(s)
				return val
			}

			if i == 0 {
				parts := strings.Split(e1.Text, separator)
				for i := range parts {
					parts[i] = strings.TrimSpace(parts[i])
				}
				rst.DinnerMinPrice = cleanAndParse(parts[0])
				rst.DinnerMaxPrice = cleanAndParse(parts[1])
			} else {
				parts := strings.Split(e1.Text, separator)
				for i := range parts {
					parts[i] = strings.TrimSpace(parts[i])
				}
				rst.LunchMinPrice = cleanAndParse(parts[0])
				rst.LunchMaxPrice = cleanAndParse(parts[1])
			}
		})
	})

	c1.OnHTML("dl.rdheader-subinfo__item", func(e *colly.HTMLElement) {
		title := strings.TrimSpace(e.ChildText("dt.rdheader-subinfo__item-title"))
		if title == "最近的車站：" {
			rst.NearestStation = strings.TrimSpace(e.ChildText(".linktree__parent-target-text"))
		}
	})

	c1.OnHTML("dl.rdheader-subinfo__item", func(e *colly.HTMLElement) {
		title := strings.TrimSpace(e.ChildText("dt.rdheader-subinfo__item-title"))
		if title == "類別：" {
			e.ForEach(".linktree__parent-target-text", func(_ int, el *colly.HTMLElement) {
				rst.Categories = append(rst.Categories, strings.TrimSpace(el.Text))
			})
		}
	})

	c1.OnHTML("table.rstinfo-table__table tr", func(e *colly.HTMLElement) {
		if strings.TrimSpace(e.ChildText("th")) == "關於兒童" {
			rst.Kids = strings.TrimSpace(e.ChildText("td p"))
		}
	})

	c1.OnHTML("p.rstdtl-top-postphoto__photo a.js-imagebox-trigger", func(e *colly.HTMLElement) {
		if len(rst.Photos) < 3 {
			if href := strings.TrimSpace(e.Attr("href")); href != "" {
				rst.Photos = append(rst.Photos, href)
			}
		}
	})

	if err := c1.Visit(url); err != nil {
		if rateLimited {
			return rst, ErrRateLimited
		}
		return rst, fmt.Errorf("fetch restaurant detail %s: %w", url, err)
	}

	if rateLimited {
		return rst, ErrRateLimited
	}
	if strings.TrimSpace(rst.Name) == "" {
		return rst, fmt.Errorf("fetch restaurant detail %s: restaurant name missing", url)
	}

	c2 := newCollector(5 * time.Second)
	c2.OnError(func(r *colly.Response, err error) {
		if r.StatusCode == 429 {
			rateLimited = true
		}
	})

	c2.OnHTML("#js-basics", func(e *colly.HTMLElement) {
		rst.Latitude, _ = strconv.ParseFloat(e.Attr("data-lat"), 64)
		rst.Longitude, _ = strconv.ParseFloat(e.Attr("data-lng"), 64)
	})

	if err := c2.Visit(url + "/dtlmap/"); err != nil {
		if rateLimited {
			return rst, ErrRateLimited
		}
		return rst, fmt.Errorf("fetch restaurant map %s: %w", url, err)
	}

	if rateLimited {
		return rst, ErrRateLimited
	}

	return rst, nil
}

// RestaurantJa holds Japanese-language fields scraped from the canonical
// tabelog.com (non-/tw/) detail page. Pillow word is not included — it lives
// on the list page and is harvested by a separate pass.
type RestaurantJa struct {
	Name           string
	Alias          string
	Address        string
	NearestStation string
	Kids           string
	Categories     []string // JP names in DOM order
}

// JaURL converts a ZH (Taiwan) restaurant URL to its Japanese equivalent
// by stripping the /tw/ prefix. Returns the input unchanged if no /tw/
// segment is present.
func JaURL(zhURL string) string {
	return strings.Replace(zhURL, "tabelog.com/tw/", "tabelog.com/", 1)
}

// FetchRstInfoJa fetches Japanese-language fields for a restaurant from its
// canonical (non-/tw/) tabelog page. Callers should pass a URL already
// transformed via JaURL.
func FetchRstInfoJa(url string) (RestaurantJa, error) {
	var rst RestaurantJa
	var rateLimited bool

	c := newCollector(5 * time.Second)
	c.OnError(func(r *colly.Response, err error) {
		if r.StatusCode == 429 {
			rateLimited = true
		}
	})

	c.OnHTML(".rdheader-rstname h2.display-name", func(e *colly.HTMLElement) {
		rst.Name = strings.TrimSpace(e.Text)
	})
	c.OnHTML(".rdheader-rstname .alias", func(e *colly.HTMLElement) {
		rst.Alias = strings.TrimSpace(e.Text)
	})
	c.OnHTML(".rstinfo-table__address", func(e *colly.HTMLElement) {
		rst.Address = strings.TrimSpace(e.Text)
	})

	c.OnHTML("dl.rdheader-subinfo__item", func(e *colly.HTMLElement) {
		title := strings.TrimSpace(e.ChildText("dt.rdheader-subinfo__item-title"))
		switch title {
		case "最寄り駅：":
			rst.NearestStation = strings.TrimSpace(e.ChildText(".linktree__parent-target-text"))
		case "ジャンル：":
			e.ForEach(".linktree__parent-target-text", func(_ int, el *colly.HTMLElement) {
				rst.Categories = append(rst.Categories, strings.TrimSpace(el.Text))
			})
		}
	})

	c.OnHTML("table.rstinfo-table__table tr", func(e *colly.HTMLElement) {
		if strings.TrimSpace(e.ChildText("th")) == "お子様連れ" {
			rst.Kids = strings.TrimSpace(e.ChildText("td p"))
		}
	})

	c.Visit(url)

	if rateLimited {
		return rst, ErrRateLimited
	}
	return rst, nil
}

// PillowEntry pairs a JP restaurant URL with its list-page PR text.
type PillowEntry struct {
	URL    string // canonical JP URL (no /tw/)
	Pillow string
}

// FetchPillowsFromListing walks a JP listing page with pagination and invokes
// sink for every <li> that has both a restaurant URL and a non-empty
// pillow (PR title). Returns ErrRateLimited if any request hit HTTP 429.
func FetchPillowsFromListing(listingURL string, sink func(PillowEntry)) error {
	visited := map[string]bool{listingURL: true}
	var rateLimited bool

	c := newCollector(5 * time.Second)
	c.OnError(func(r *colly.Response, err error) {
		if r.StatusCode == 429 {
			rateLimited = true
		}
	})

	c.OnHTML("div.list-rst.js-rst-cassette-wrap", func(e *colly.HTMLElement) {
		href := strings.TrimSpace(e.ChildAttr("a.list-rst__rst-name-target.cpy-rst-name", "href"))
		pillow := strings.TrimSpace(e.ChildText(".list-rst__pr-title.cpy-pr-title"))
		if href == "" || pillow == "" {
			return
		}
		sink(PillowEntry{URL: href, Pillow: pillow})
	})

	c.OnHTML("a.c-pagination__arrow--next", func(e *colly.HTMLElement) {
		next := e.Attr("href")
		if next == "" || visited[next] {
			return
		}
		visited[next] = true
		e.Request.Visit(next)
	})

	c.Visit(listingURL)

	if rateLimited {
		return ErrRateLimited
	}
	return nil
}
