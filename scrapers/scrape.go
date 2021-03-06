package scrapers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xbapps/xbvr-scrapers/helpers"

	"github.com/d5/tengo/v2"
	"github.com/d5/tengo/v2/stdlib"
	"github.com/gocolly/colly/v2"
	"github.com/mozillazg/go-slugify"
)

var userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/73.0.3683.103 Safari/537.36"

type NeededVars struct {
	VarName     string   `json:"var_name"`
	CollyMethod string   `json:"colly_method"`
	CollyArgs   []string `json:"colly_args"`
}

type ScraperDefinition struct {
	ScraperID      string   `json:"scraper_id"`
	SiteID         string   `json:"site_id"`
	Studio         string   `json:"studio"`
	SiteIcon       string   `json:"site_icon"`
	AllowedDomains []string `json:"allowed_domains"`
	StartURL       string   `json:"start_url"`
	SiteOnhtml     struct {
		Selector        string   `json:"selector"`
		VisitAttr       string   `json:"visit_attr"`
		SkipKnown       bool     `json:"skip_known"`
		SkipURLContains []string `json:"skip_url_contains"`
	} `json:"site_onhtml"`
	PaginationOnhtml struct {
		Selector  string `json:"selector"`
		VisitAttr string `json:"visit_attr"`
		SkipKnown bool   `json:"skip_known"`
	} `json:"pagination_onhtml"`
	SceneOnhtml struct {
		Selector        string       `json:"selector"`
		TransferToExtra bool         `json:"transfer_to_extra"`
		NeededVars      []NeededVars `json:"needed_vars"`
	} `json:"scene_onhtml"`
	ExtraOnhtml struct {
		Selector   string       `json:"selector"`
		Parser     string       `json:"parser"`
		NeededVars []NeededVars `json:"needed_vars"`
	} `json:"extra_onhtml,omitempty"`
}

type ScrapedScene struct {
	SceneID     string   `json:"_id"`
	SiteID      string   `json:"scene_id"`
	SceneType   string   `json:"scene_type"`
	Title       string   `json:"title"`
	Studio      string   `json:"studio"`
	Site        string   `json:"site"`
	Covers      []string `json:"covers"`
	Gallery     []string `json:"gallery"`
	Tags        []string `json:"tags"`
	Cast        []string `json:"cast"`
	Filenames   []string `json:"filename"`
	Duration    int      `json:"duration"`
	Synopsis    string   `json:"synopsis"`
	Released    string   `json:"released"`
	HomepageURL string   `json:"homepage_url"`
}

func arrayToInterface(a []string) []interface{} {
	var i []interface{}
	for _, s := range a {
		i = append(i, s)
	}
	return i
}

func interfaceToArray(i []interface{}) []string {
	var s []string
	for _, x := range i {
		s = append(s, strings.TrimSpace(x.(string)))
	}
	return s
}

func createCollector(domains ...string) *colly.Collector {
	c := colly.NewCollector(
		colly.AllowedDomains(domains...),
		colly.CacheDir("./scraper_cache"),
		colly.UserAgent(userAgent),
	)

	c = createCallbacks(c)
	return c
}

func cloneCollector(c *colly.Collector) *colly.Collector {
	x := c.Clone()
	x = createCallbacks(x)
	return x
}

func createCallbacks(c *colly.Collector) *colly.Collector {
	const maxRetries = 15

	c.OnRequest(func(r *colly.Request) {
		attempt := r.Ctx.GetAny("attempt")

		if attempt == nil {
			r.Ctx.Put("attempt", 1)
		}

		fmt.Println("visiting", r.URL.String())
	})

	c.OnError(func(r *colly.Response, err error) {
		attempt := r.Ctx.GetAny("attempt").(int)

		if r.StatusCode == 429 {
			fmt.Println("Error:", r.StatusCode, err)

			if attempt <= maxRetries {
				unCache(r.Request.URL.String(), c.CacheDir)
				fmt.Println("Waiting 2 seconds before next request...")
				r.Ctx.Put("attempt", attempt+1)
				time.Sleep(2 * time.Second)
				r.Request.Retry()
			}
		}
	})

	return c
}

func unCache(URL string, cacheDir string) {
	sum := sha1.Sum([]byte(URL))
	hash := hex.EncodeToString(sum[:])
	dir := path.Join(cacheDir, hash[:2])
	filename := path.Join(dir, hash)
	if err := os.Remove(filename); err != nil {
		fmt.Println(err)
	}
}

func getScript(s string) *tengo.Script {
	scriptContents, err := ioutil.ReadFile(s)
	if err != nil {
		panic(err)
	}
	script := tengo.NewScript(scriptContents)
	tengoMods := stdlib.GetModuleMap("fmt", "text", "times")
	tengoMods.AddMap(helpers.GetModuleMap(helpers.AllModuleNames()...))
	script.SetImports(tengoMods)
	return script
}

func processVars(nv []NeededVars, e *colly.HTMLElement, script *tengo.Script) {
	for _, v := range nv {
		switch v.CollyMethod {
		case "ChildText":
			val := e.ChildText(v.CollyArgs[0])
			_ = script.Add(v.VarName, val)
		case "ChildTexts":
			val := e.ChildTexts(v.CollyArgs[0])
			_ = script.Add(v.VarName, arrayToInterface(val))
		case "ChildAttr":
			val := e.ChildAttr(v.CollyArgs[0], v.CollyArgs[1])
			_ = script.Add(v.VarName, val)
		case "ChildAttrs":
			val := e.ChildAttrs(v.CollyArgs[0], v.CollyArgs[1])
			_ = script.Add(v.VarName, arrayToInterface(val))
		}
	}
	_ = script.Add("homepageURL", strings.Split(e.Request.URL.String(), "?")[0])
	_ = script.Add("fullHomepageURL", e.Request.URL.String())
}

func Scrape(wg *sync.WaitGroup, configFile string, parserFile string, out chan<- ScrapedScene) {
	defer wg.Done()

	scraperConfig, err := ioutil.ReadFile(configFile)
	if err != nil {
		panic(err)
	}

	var scraper ScraperDefinition
	json.Unmarshal(scraperConfig, &scraper)

	sceneCollector := createCollector(scraper.AllowedDomains...)
	siteCollector := createCollector(scraper.AllowedDomains...)

	extraCollector := cloneCollector(sceneCollector)

	sceneScript := getScript(parserFile)

	sceneCollector.OnHTML(scraper.SceneOnhtml.Selector, func(e *colly.HTMLElement) {
		scene := ScrapedScene{}
		processVars(scraper.SceneOnhtml.NeededVars, e, sceneScript)

		// run the script
		parsed, err := sceneScript.RunContext(context.Background())
		if err != nil {
			panic(err)
		}

		scene.SceneType = "VR"
		scene.Site = scraper.SiteID
		scene.SiteID = strings.TrimSpace(parsed.Get("siteID").String())
		scene.Studio = scraper.Studio

		// retrieve values from script
		scene.Cast = interfaceToArray(parsed.Get("cast").Array())
		covers := parsed.Get("coverURL")
		if covers.ValueType() == "string" {
			scene.Covers = append(scene.Covers, strings.TrimSpace(covers.String()))
		} else {
			scene.Covers = interfaceToArray(covers.Array())
			fmt.Println(covers.Array())
		}
		scene.Duration = parsed.Get("duration").Int()
		scene.Filenames = interfaceToArray(parsed.Get("filenames").Array())
		scene.Gallery = interfaceToArray(parsed.Get("galleryURLS").Array())
		scene.HomepageURL = strings.TrimSpace(parsed.Get("homepageURL").String())
		scene.Released = strings.TrimSpace(parsed.Get("released").String())
		scene.SceneID = slugify.Slugify(scene.Site + "-" + scene.SiteID)
		scene.Synopsis = strings.TrimSpace(parsed.Get("synopsis").String())
		scene.Tags = interfaceToArray(parsed.Get("tags").Array())
		scene.Title = strings.TrimSpace(parsed.Get("title").String())

		if scraper.SceneOnhtml.TransferToExtra {
			ctx := colly.NewContext()
			ctx.Put("scene", scene)

			extraURL := parsed.Get("extraURL").String()
			extraCollector.Request("GET", extraURL, nil, ctx, nil)
		} else {
			out <- scene
		}
	})

	siteCollector.OnHTML(scraper.SiteOnhtml.Selector, func(e *colly.HTMLElement) {
		u := e.Request.AbsoluteURL(e.Attr(scraper.SiteOnhtml.VisitAttr))
		shouldVisit := true
		if scraper.SiteOnhtml.SkipURLContains != nil {
			for _, s := range scraper.SiteOnhtml.SkipURLContains {
				if strings.Contains(u, s) {
					shouldVisit = false
					break
				}
			}
		}
		if shouldVisit {
			sceneCollector.Visit(u)
		}
	})

	siteCollector.OnHTML(scraper.PaginationOnhtml.Selector, func(e *colly.HTMLElement) {
		u := e.Request.AbsoluteURL(e.Attr(scraper.PaginationOnhtml.VisitAttr))
		siteCollector.Visit(u)
	})

	if scraper.ExtraOnhtml.Selector != "" {
		var extraScript *tengo.Script
		if scraper.ExtraOnhtml.Parser != "" {
			extraScript = getScript(filepath.Join(filepath.Dir(parserFile), scraper.ExtraOnhtml.Parser))
		}
		extraCollector.OnHTML(scraper.ExtraOnhtml.Selector, func(e *colly.HTMLElement) {
			processVars(scraper.ExtraOnhtml.NeededVars, e, extraScript)

			// run the script
			parsed, err := extraScript.RunContext(context.Background())
			if err != nil {
				panic(err)
			}

			scene := e.Request.Ctx.GetAny("scene").(ScrapedScene)

			if len(scene.Filenames) == 0 {
				scene.Filenames = interfaceToArray(parsed.Get("filenames").Array())
			}

			out <- scene
		})
	}

	// If a site only has scenes, site_onhtml can be omitted and we'll start
	// with the sceneCollector instead
	if scraper.SiteOnhtml.Selector != "" {
		siteCollector.Visit(scraper.StartURL)
	} else {
		sceneCollector.Visit(scraper.StartURL)
	}
}
