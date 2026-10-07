// Package rates pulls market exchange rates (National Bank of Kazakhstan)
// for summary reports. These rates have nothing to do with the user's
// actual operations — there the rate is entered manually.
package rates

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const nbrkURL = "https://nationalbank.kz/rss/get_rates.cfm?fdate="

// Quote — one currency's rate against tenge on a given date.
type Quote struct {
	Code string
	Rate float64 // how many KZT per 1 unit
	Date time.Time
}

// get_rates.cfm responds with <rates><item>…, rates_all.xml responds with <rss><channel><item>…
type rssFeed struct {
	Items        []rssItem `xml:"item"`
	ChannelItems []rssItem `xml:"channel>item"`
}

func (f rssFeed) items() []rssItem {
	if len(f.Items) > 0 {
		return f.Items
	}
	return f.ChannelItems
}

type rssItem struct {
	Title       string `xml:"title"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
	Quant       string `xml:"quant"`
}

// FetchNBRK downloads the official National Bank of Kazakhstan rates for the given day.
func FetchNBRK(ctx context.Context, client *http.Client, day time.Time) ([]Quote, error) {
	url := nbrkURL + day.Format("02.01.2006")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "aksha-bitpesin/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting NBRK rates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NBRK responded with %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parsing NBRK response: %w", err)
	}

	items := feed.items()
	out := make([]Quote, 0, len(items))
	for _, it := range items {
		code := strings.ToUpper(strings.TrimSpace(it.Title))
		if len(code) != 3 {
			continue
		}
		rate, err := strconv.ParseFloat(strings.TrimSpace(it.Description), 64)
		if err != nil || rate <= 0 {
			continue
		}
		if q, err := strconv.ParseFloat(strings.TrimSpace(it.Quant), 64); err == nil && q > 0 {
			rate /= q // NBRK quotes some minor currencies per 10/100/1000 units
		}
		out = append(out, Quote{Code: code, Rate: rate, Date: day})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("NBRK returned no rates for %s", day.Format("02.01.2006"))
	}
	return out, nil
}
