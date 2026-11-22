// Package epub reads the text of an EPUB book: its title and author, and the
// documents of its reading order as paragraphs, with the table of contents
// telling which documents begin a chapter.
package epub

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

// Book is the text of an EPUB.
type Book struct {
	Title    string
	Author   string
	Sections []Section // the documents of the reading order
}

// Section is one document of the reading order.
type Section struct {
	// Title is set when the section begins a chapter: the label of its entry
	// in the table of contents or, in a book without one, its own heading.
	Title      string
	Paragraphs []string
}

// IsEPUB reports whether the file name is that of an EPUB book.
func IsEPUB(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".epub")
}

// Read opens the EPUB at path and returns its text.
func Read(path string) (Book, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return Book{}, fmt.Errorf("epub %s: %w", path, err)
	}
	defer zr.Close()
	book, err := read(&zr.Reader)
	if err != nil {
		return Book{}, fmt.Errorf("epub %s: %w", path, err)
	}
	return book, nil
}

// container is META-INF/container.xml.
type container struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

// opf is the package document.
type opf struct {
	Title    []string `xml:"metadata>title"`
	Creators []string `xml:"metadata>creator"`
	Items    []struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		MediaType  string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"manifest>item"`
	Spine struct {
		TOC   string `xml:"toc,attr"`
		Items []struct {
			IDRef  string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

func read(zr *zip.Reader) (Book, error) {
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}

	var c container
	if err := decodeXML(files, "META-INF/container.xml", &c); err != nil {
		return Book{}, err
	}
	if len(c.Rootfiles) == 0 || c.Rootfiles[0].FullPath == "" {
		return Book{}, errors.New("META-INF/container.xml names no package document")
	}
	opfPath := c.Rootfiles[0].FullPath
	var pkg opf
	if err := decodeXML(files, opfPath, &pkg); err != nil {
		return Book{}, err
	}

	book := Book{Title: first(pkg.Title), Author: strings.Join(nonEmpty(pkg.Creators), ", ")}

	// Manifest: id → path inside the archive.
	type item struct{ path, mediaType string }
	items := make(map[string]item, len(pkg.Items))
	var navPath, ncxPath string
	for _, it := range pkg.Items {
		p := resolve(opfPath, it.Href)
		items[it.ID] = item{path: p, mediaType: it.MediaType}
		if strings.Contains(" "+it.Properties+" ", " nav ") {
			navPath = p
		}
		if it.ID == pkg.Spine.TOC || (ncxPath == "" && it.MediaType == "application/x-dtbncx+xml") {
			ncxPath = p
		}
	}

	// The table of contents: document path → label of its first entry.
	var labels map[string]string
	if navPath != "" {
		labels = readNav(files, navPath)
	}
	if len(labels) == 0 && ncxPath != "" {
		labels = readNCX(files, ncxPath)
	}

	for _, ref := range pkg.Spine.Items {
		it, ok := items[ref.IDRef]
		if !ok || strings.EqualFold(ref.Linear, "no") || it.path == navPath {
			continue
		}
		if it.mediaType != "" && !strings.Contains(it.mediaType, "html") {
			continue
		}
		f, ok := files[it.path]
		if !ok {
			continue
		}
		blocks, err := readDocument(f)
		if err != nil && len(blocks) == 0 {
			return Book{}, fmt.Errorf("%s: %w", it.path, err)
		}
		book.Sections = append(book.Sections, section(blocks, labels, it.path))
	}
	if len(book.Sections) == 0 {
		return Book{}, errors.New("the book has no text documents in its reading order")
	}
	return book, nil
}

// section turns the blocks of a document into a Section. With a table of
// contents the documents it names begin chapters; without one, every
// document that opens with a heading does.
func section(blocks []block, labels map[string]string, docPath string) Section {
	// The headings a document opens with are its title, not its text.
	lead := 0
	for lead < len(blocks) && blocks[lead].heading {
		lead++
	}
	var headings []string
	for _, b := range blocks[:lead] {
		headings = append(headings, b.text)
	}

	var s Section
	switch {
	case labels != nil:
		s.Title = labels[docPath]
	case lead > 0:
		s.Title = strings.Join(headings, ". ")
	}
	if s.Title != "" {
		blocks = blocks[lead:]
	}
	for _, b := range blocks {
		s.Paragraphs = append(s.Paragraphs, b.text)
	}
	return s
}

// resolve returns the archive path of href, which is relative to the file
// at base. The fragment is dropped.
func resolve(base, href string) string {
	href, _, _ = strings.Cut(href, "#")
	if unescaped, err := url.PathUnescape(href); err == nil {
		href = unescaped
	}
	return strings.TrimPrefix(path.Join(path.Dir(base), href), "/")
}

func decodeXML(files map[string]*zip.File, name string, v any) error {
	f, ok := files[name]
	if !ok {
		return fmt.Errorf("%s is missing", name)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	defer rc.Close()
	if err := newDecoder(rc).Decode(v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// newDecoder returns a decoder that accepts the XHTML found in real books:
// HTML entities and unclosed void elements.
func newDecoder(r io.Reader) *xml.Decoder {
	d := xml.NewDecoder(r)
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	return d
}

func first(values []string) string {
	if v := nonEmpty(values); len(v) > 0 {
		return v[0]
	}
	return ""
}

func nonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v = collapse(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// collapse joins the words of s with single spaces.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }
