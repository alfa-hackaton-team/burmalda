package feeds

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const ParserVersion = "feed-entry-v1"
const MaxEntries = 500

type Entry struct {
	ExternalID   string
	CanonicalURL string
	Title        string
	Body         string
	PublishedAt  *time.Time
}

type ParsedEntry struct {
	Entry
	EntryNo int
}

type ParseFailure struct {
	EntryNo int
	Err     error
}

func (e Entry) Identity() (kind, key string) {
	if e.ExternalID != "" {
		return "external_id", e.ExternalID
	}
	return "canonical_url", e.CanonicalURL
}

func (e Entry) ContentHash() string {
	var published string
	if e.PublishedAt != nil {
		published = e.PublishedAt.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(e.Title + "\x00" + e.Body + "\x00" + published))
	return hex.EncodeToString(sum[:])
}

type rssFeed struct {
	Channel *struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	GUID        string `xml:"guid"`
	Link        string `xml:"link"`
	Title       string `xml:"title"`
	Description string `xml:"description"`
	Content     string `xml:"encoded"`
	PubDate     string `xml:"pubDate"`
}

type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Links     []atomLink `xml:"link"`
	Title     atomText   `xml:"title"`
	Content   atomText   `xml:"content"`
	Summary   atomText   `xml:"summary"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomText struct {
	Type  string `xml:"type,attr"`
	Src   string `xml:"src,attr"`
	Text  string `xml:",chardata"`
	Inner string `xml:",innerxml"`
}

func (t atomText) value() string {
	if t.Type == "xhtml" {
		return t.Inner
	}
	if t.Type == "text" || t.Type == "" {
		return html.EscapeString(t.Text)
	}
	return t.Text
}

func Parse(data []byte, feedURL, adapterType string) ([]Entry, error) {
	return ParseWithOptions(data, feedURL, adapterType, false)
}

func ParseWithOptions(data []byte, feedURL, adapterType string, allowEmptyBody bool) ([]Entry, error) {
	return ParseWithLimit(data, feedURL, adapterType, allowEmptyBody, MaxEntries)
}

func ParseWithLimit(data []byte, feedURL, adapterType string, allowEmptyBody bool, maxEntries int) ([]Entry, error) {
	parsed, failures, err := ParseEntriesWithLimit(data, feedURL, adapterType, allowEmptyBody, maxEntries)
	if err != nil {
		return nil, err
	}
	if len(failures) > 0 {
		return nil, failures[0].Err
	}
	entries := make([]Entry, 0, len(parsed))
	for _, item := range parsed {
		entries = append(entries, item.Entry)
	}
	return entries, nil
}

func ParseEntriesWithLimit(data []byte, feedURL, adapterType string, allowEmptyBody bool, maxEntries int) ([]ParsedEntry, []ParseFailure, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = charset.NewReaderLabel
	var root xml.StartElement
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				return nil, nil, fmt.Errorf("empty feed")
			}
			return nil, nil, fmt.Errorf("read feed XML: %w", err)
		}
		if start, ok := token.(xml.StartElement); ok {
			root = start
			break
		}
	}
	var entries []ParsedEntry
	var failures []ParseFailure
	switch adapterType {
	case "rss":
		if root.Name.Local != "rss" {
			return nil, nil, fmt.Errorf("expected RSS feed, got %s", root.Name.Local)
		}
		var feed rssFeed
		if err := decoder.DecodeElement(&feed, &root); err != nil {
			return nil, nil, fmt.Errorf("parse RSS: %w", err)
		}
		if feed.Channel == nil {
			return nil, nil, fmt.Errorf("RSS channel is missing")
		}
		if len(feed.Channel.Items) > maxEntries {
			return nil, nil, fmt.Errorf("RSS feed exceeds %d entries", maxEntries)
		}
		for i, item := range feed.Channel.Items {
			body := item.Content
			if strings.TrimSpace(body) == "" {
				body = item.Description
			}
			entry, err := makeEntry(feedURL, item.GUID, item.Link, item.Title, body, item.PubDate, allowEmptyBody)
			if err != nil {
				failures = append(failures, ParseFailure{EntryNo: i + 1, Err: fmt.Errorf("RSS item %d: %w", i+1, err)})
				continue
			}
			entries = append(entries, ParsedEntry{Entry: entry, EntryNo: i + 1})
		}
	case "atom":
		if root.Name.Local != "feed" || root.Name.Space != "http://www.w3.org/2005/Atom" {
			return nil, nil, fmt.Errorf("expected Atom feed, got %s", root.Name.Local)
		}
		var feed atomFeed
		if err := decoder.DecodeElement(&feed, &root); err != nil {
			return nil, nil, fmt.Errorf("parse Atom: %w", err)
		}
		if len(feed.Entries) > maxEntries {
			return nil, nil, fmt.Errorf("Atom feed exceeds %d entries", maxEntries)
		}
		for i, item := range feed.Entries {
			if item.Content.Src != "" {
				failures = append(failures, ParseFailure{EntryNo: i + 1, Err: fmt.Errorf("Atom entry %d: external content is unsupported", i+1)})
				continue
			}
			body := item.Content.value()
			if strings.TrimSpace(body) == "" {
				body = item.Summary.value()
			}
			link := ""
			for _, candidate := range item.Links {
				if candidate.Rel == "" || candidate.Rel == "alternate" {
					link = candidate.Href
					break
				}
			}
			published := item.Published
			if published == "" {
				published = item.Updated
			}
			entry, err := makeEntry(feedURL, item.ID, link, item.Title.value(), body, published, allowEmptyBody)
			if err != nil {
				failures = append(failures, ParseFailure{EntryNo: i + 1, Err: fmt.Errorf("Atom entry %d: %w", i+1, err)})
				continue
			}
			entries = append(entries, ParsedEntry{Entry: entry, EntryNo: i + 1})
		}
	default:
		return nil, nil, fmt.Errorf("unsupported feed adapter %q", adapterType)
	}
	seen := make(map[string]string, len(entries))
	unique := make([]ParsedEntry, 0, len(entries))
	for _, item := range entries {
		kind, key := item.Identity()
		identity := kind + "\x00" + key
		if previousHash, exists := seen[identity]; exists {
			if previousHash != item.ContentHash() {
				failures = append(failures, ParseFailure{EntryNo: item.EntryNo, Err: fmt.Errorf("feed contains conflicting entries for the same identity")})
			}
			continue
		}
		seen[identity] = item.ContentHash()
		unique = append(unique, item)
	}
	return unique, failures, nil
}

func makeEntry(feedURL, id, link, title, rawBody, date string, allowEmptyBody bool) (Entry, error) {
	title, err := plainText(title)
	if err != nil {
		return Entry{}, fmt.Errorf("normalize title: %w", err)
	}
	body, err := plainText(rawBody)
	if err != nil {
		return Entry{}, fmt.Errorf("normalize body: %w", err)
	}
	if body == "" && !allowEmptyBody {
		return Entry{}, fmt.Errorf("article text is empty")
	}
	canonical, err := articleURL(feedURL, link)
	if err != nil {
		return Entry{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" && canonical == "" {
		return Entry{}, fmt.Errorf("entry has no stable ID or URL")
	}
	return Entry{ExternalID: id, CanonicalURL: canonical, Title: title,
		Body: body, PublishedAt: parseDate(date)}, nil
}

func articleURL(feedURL, link string) (string, error) {
	if strings.TrimSpace(link) == "" {
		return "", nil
	}
	base, err := url.Parse(feedURL)
	if err != nil {
		return "", fmt.Errorf("invalid feed URL: %w", err)
	}
	reference, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return "", fmt.Errorf("invalid article URL")
	}
	resolved := base.ResolveReference(reference)
	resolved.Fragment = ""
	if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Host == "" || resolved.User != nil {
		return "", fmt.Errorf("article URL must be http(s)")
	}
	return resolved.String(), nil
}

func parseDate(raw string) *time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z,
		time.RFC1123, time.RFC822Z, time.RFC822} {
		if value, err := time.Parse(layout, strings.TrimSpace(raw)); err == nil {
			utc := value.UTC()
			return &utc
		}
	}
	return nil
}

func plainText(raw string) (string, error) {
	root, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return "", err
	}
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		block := false
		if node.Type == html.ElementNode {
			switch node.Data {
			case "script", "style", "nav", "header", "footer", "aside", "form", "noscript":
				return
			case "p", "div", "article", "section", "li", "ul", "ol", "br", "h1", "h2", "h3", "blockquote":
				block = true
			}
		}
		if block {
			text.WriteByte(' ')
		}
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
		if block {
			text.WriteByte(' ')
		}
	}
	visit(root)
	return strings.Join(strings.Fields(text.String()), " "), nil
}
