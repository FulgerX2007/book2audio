package epub

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"strings"
)

// block is one paragraph of a document.
type block struct {
	text    string
	heading bool // the text of an h1–h3 element
}

// blockTags are the elements whose start and end separate paragraphs.
var blockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "aside": true, "header": true, "footer": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"li": true, "ul": true, "ol": true, "dl": true, "dt": true, "dd": true,
	"blockquote": true, "pre": true, "figure": true, "figcaption": true,
	"table": true, "tr": true, "td": true, "th": true, "caption": true, "hr": true, "body": true,
}

// skipTags are the elements whose text is not part of the book as it is
// read aloud: code and styles, navigation, and footnote marks.
var skipTags = map[string]bool{"head": true, "script": true, "style": true, "nav": true, "sup": true, "svg": true, "math": true}

var headingTags = map[string]bool{"h1": true, "h2": true, "h3": true}

// readDocument returns the paragraphs of an XHTML document. A document that
// breaks off in the middle yields the paragraphs read so far and the error.
func readDocument(f *zip.File) ([]block, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return parseDocument(rc)
}

func parseDocument(r io.Reader) ([]block, error) {
	d := newDecoder(r)
	var (
		blocks  []block
		text    strings.Builder
		skip    int // depth inside elements of skipTags
		heading int // depth inside h1–h3
		isHead  bool
	)
	flush := func() {
		if t := collapse(text.String()); t != "" {
			blocks = append(blocks, block{text: t, heading: isHead})
		}
		text.Reset()
		isHead = heading > 0
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			flush()
			return blocks, nil
		}
		if err != nil {
			flush()
			return blocks, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			switch {
			case skipTags[name]:
				skip++
			case name == "br":
				text.WriteByte(' ')
			case blockTags[name]:
				flush()
				if headingTags[name] {
					heading++
					isHead = true
				}
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			switch {
			case skipTags[name]:
				if skip > 0 {
					skip--
				}
			case blockTags[name]:
				if headingTags[name] && heading > 0 {
					heading--
				}
				flush()
			}
		case xml.CharData:
			if skip == 0 {
				text.Write(t)
			}
		}
	}
}

// readNav reads an EPUB 3 navigation document: the links of its table of
// contents, as document path → label of the first link to it.
func readNav(files map[string]*zip.File, navPath string) map[string]string {
	f, ok := files[navPath]
	if !ok {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()

	d := newDecoder(rc)
	labels := map[string]string{}
	var (
		depth, tocDepth int // tocDepth: depth of the toc <nav>, 0 outside it
		navs            int // <nav> elements seen
		href            string
		inLink          bool
		label           strings.Builder
	)
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch strings.ToLower(t.Name.Local) {
			case "nav":
				navs++
				// The toc nav is marked epub:type="toc"; a document with a
				// single unmarked nav is read as well.
				kind := attr(t, "type")
				if tocDepth == 0 && (strings.Contains(" "+kind+" ", " toc ") || (kind == "" && navs == 1)) {
					tocDepth = depth
				}
			case "a":
				if tocDepth > 0 {
					href, inLink = attr(t, "href"), true
					label.Reset()
				}
			}
		case xml.EndElement:
			switch strings.ToLower(t.Name.Local) {
			case "nav":
				if depth == tocDepth {
					tocDepth = 0
				}
			case "a":
				if inLink {
					addLabel(labels, resolve(navPath, href), label.String())
					inLink = false
				}
			}
			depth--
		case xml.CharData:
			if inLink {
				label.Write(t)
			}
		}
	}
	return labels
}

// readNCX reads an EPUB 2 table of contents (toc.ncx).
func readNCX(files map[string]*zip.File, ncxPath string) map[string]string {
	f, ok := files[ncxPath]
	if !ok {
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()

	d := newDecoder(rc)
	labels := map[string]string{}
	var (
		inMap, inText bool
		label         strings.Builder
		pending       []string // labels of the open navPoints, innermost last
	)
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "navmap":
				inMap = true
			case "navpoint":
				pending = append(pending, "")
			case "text":
				if inMap && len(pending) > 0 {
					inText = true
					label.Reset()
				}
			case "content":
				if inMap && len(pending) > 0 {
					addLabel(labels, resolve(ncxPath, attr(t, "src")), pending[len(pending)-1])
				}
			}
		case xml.EndElement:
			switch strings.ToLower(t.Name.Local) {
			case "navmap":
				inMap = false
			case "navpoint":
				if len(pending) > 0 {
					pending = pending[:len(pending)-1]
				}
			case "text":
				if inText {
					pending[len(pending)-1] = label.String()
					inText = false
				}
			}
		case xml.CharData:
			if inText {
				label.Write(t)
			}
		}
	}
	return labels
}

// addLabel records the label of a document unless an earlier entry of the
// table of contents already named it.
func addLabel(labels map[string]string, docPath, label string) {
	label = collapse(label)
	if docPath == "" || label == "" {
		return
	}
	if _, seen := labels[docPath]; !seen {
		labels[docPath] = label
	}
}

// attr returns the value of the attribute with the given local name.
func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if strings.EqualFold(a.Name.Local, name) {
			return a.Value
		}
	}
	return ""
}
