// Package epubtest builds small EPUB files for tests.
package epubtest

import (
	"archive/zip"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Doc is one document of the reading order.
type Doc struct {
	Name string // file name inside OEBPS/, e.g. "ch1.xhtml"
	Body string // XHTML placed inside <body>
	TOC  string // label in the table of contents; empty: not listed
}

// Spec describes an EPUB to build.
type Spec struct {
	Title  string
	Author string
	Docs   []Doc
	// TOC chooses the table of contents: "ncx" (EPUB 2, the default),
	// "nav" (EPUB 3) or "none".
	TOC string
}

// Write builds the EPUB at path.
func Write(t testing.TB, path string, spec Spec) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	add := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`)

	var manifest, spine, navPoints, navLinks strings.Builder
	for i, d := range spec.Docs {
		fmt.Fprintf(&manifest, `<item id="d%d" href="%s" media-type="application/xhtml+xml"/>`+"\n", i, d.Name)
		fmt.Fprintf(&spine, `<itemref idref="d%d"/>`+"\n", i)
		if d.TOC != "" {
			fmt.Fprintf(&navPoints, `<navPoint id="n%d"><navLabel><text>%s</text></navLabel><content src="%s#top"/></navPoint>`+"\n", i, d.TOC, d.Name)
			fmt.Fprintf(&navLinks, `<li><a href="%s">%s</a></li>`+"\n", d.Name, d.TOC)
		}
		add("OEBPS/"+d.Name, `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>ignored</title><style>p { margin: 0 }</style></head>
<body>`+d.Body+`</body></html>`)
	}

	spineAttr := ""
	switch spec.TOC {
	case "", "ncx":
		manifest.WriteString(`<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>` + "\n")
		spineAttr = ` toc="ncx"`
		add("OEBPS/toc.ncx", `<?xml version="1.0"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/"><docTitle><text>ignored</text></docTitle>
<navMap>`+navPoints.String()+`</navMap></ncx>`)
	case "nav":
		manifest.WriteString(`<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>` + "\n")
		spine.WriteString(`<itemref idref="nav"/>` + "\n")
		add("OEBPS/nav.xhtml", `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body>
<nav epub:type="landmarks"><ol><li><a href="nowhere.xhtml">Cover</a></li></ol></nav>
<nav epub:type="toc"><h1>Contents</h1><ol>`+navLinks.String()+`</ol></nav></body></html>`)
	case "none":
	default:
		t.Fatalf("epubtest: unknown TOC kind %q", spec.TOC)
	}

	add("OEBPS/content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:title>`+spec.Title+`</dc:title><dc:creator>`+spec.Author+`</dc:creator>
</metadata>
<manifest>`+manifest.String()+`</manifest>
<spine`+spineAttr+`>`+spine.String()+`</spine>
</package>`)

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// Sample is a small book: a title page outside the table of contents, two
// chapters, the second one split over two documents.
func Sample() Spec {
	return Spec{
		Title:  "The Tide Tables",
		Author: "A. Keeper",
		Docs: []Doc{
			{Name: "title.xhtml", Body: `<h1>The Tide Tables</h1><p>A small book for tests.</p>`},
			{Name: "ch1.xhtml", TOC: "Chapter 1: High Water", Body: `<h2 id="top">Chapter 1</h2><h3>High Water</h3>
<p>The first paragraph of chapter one<sup>1</sup> has a <em>word</em> in italics.</p>
<p>The second paragraph<br/>is broken in two lines &amp; uses an entity&nbsp;too.</p>`},
			{Name: "ch2.xhtml", TOC: "Chapter 2: Low Water", Body: `<h2>Chapter 2: Low Water</h2>
<div><p>Chapter two opens here.</p><blockquote>A quoted line.</blockquote></div>
<ul><li>First item.</li><li>Second item.</li></ul>`},
			{Name: "ch2b.xhtml", Body: `<p>Chapter two goes on in another file.</p><script>ignored()</script>`},
		},
	}
}
