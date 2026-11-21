#!/bin/sh
# Regenerates the PDF fixtures used by the tests. The generated files are
# committed, so this script is needed only when a fixture has to change.
#
# Requires: pdflatex (with fancyhdr), pdftoppm, img2pdf.
#
#   sample.pdf  text PDF: a table of contents page, an introduction before
#               the first heading, four chapters (chapter 2 starts in the
#               middle of a page), a running header and page numbers.
#   scan.pdf    page 3 of sample.pdf as an image, without a text layer.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/sample.tex" <<'EOF'
\documentclass[12pt]{article}
\usepackage[a5paper,margin=18mm,headheight=15pt]{geometry}
\usepackage{fancyhdr}
\pagestyle{fancy}
\fancyhf{}
\lhead{A Short Field Guide to Lighthouses}
\cfoot{\thepage}
\setlength{\parindent}{0pt}
\setlength{\parskip}{8pt}
\newcommand{\heading}[1]{\par\bigskip{\large\bfseries #1}\par\medskip}
\pdfinfo{/Title (A Short Field Guide to Lighthouses) /Author (book2audio test fixture)}
\begin{document}

{\Large\bfseries Contents}

Chapter 1: The Lamp Room

Chapter 2: Keeping the Log

Chapter 3: Fog Signals

Chapter 4: The Relief Boat

\newpage

This guide was written for the tests of a small program. It describes an
imaginary lighthouse and the people who look after it. Nothing in it is
meant as advice for real keepers, and every name in it is invented.

The introduction is short on purpose. It stands before the first heading,
so a careful reader of the file should treat it as a part of its own.

\heading{Chapter 1: The Lamp Room}

The lamp room sits at the top of ninety-two steps. Every evening the keeper
climbs them with a can of oil, a clean cloth and a well-thumbed note-book.
The glass is wiped first, because salt gathers on it during the day and a
dull lens throws a dull beam.

When the wick is trimmed the keeper lights it and waits. The flame needs a
quarter of an hour to settle, and nobody hurries it. Only then is the great
lens set turning, slowly, on its bed of rollers.

On clear nights the beam can be seen from the far side of the bay. Fishing
crews say that it looks like a patient hand, waving them home.

\newpage

By morning the oil is low and the keeper is tired. The lamp is put out at
sunrise, the curtains are drawn against the day, and the brass is polished
before breakfast.

\heading{Chapter 2: Keeping the Log}

Every keeper writes in the log. The entries are plain: the hour the lamp
was lit, the state of the sea, the direction of the wind and the number of
ships that passed.

A good log is boring. When a page has only weather on it, the night went
well. The pages that people read years later are the other ones, written
in a hurried hand, with a blot where the pen was put down too fast.

The log is kept in a tin box under the stairs, wrapped in oil-cloth,
because paper and sea air are old enemies.

\newpage

\heading{Chapter 3: Fog Signals}

Fog arrives without a sound and takes the beam away. On those nights the
keeper winds the fog bell, which strikes twice every minute until the air
is clear again.

The bell is hard work. Its weight must be cranked up by hand every four
hours, and the keeper sleeps in a chair beside it with one ear open.

\heading{Chapter 4: The Relief Boat}

Once a month the relief boat comes out with letters, flour and a fresh
keeper. The old keeper goes ashore for a fortnight and forgets, for a
while, how many steps there are.

Then the boat returns, and the climbing begins again.

\end{document}
EOF

(cd "$tmp" && pdflatex -interaction=batchmode -halt-on-error sample.tex >/dev/null)
cp "$tmp/sample.pdf" "$here/sample.pdf"

pdftoppm -r 200 -png -singlefile -f 3 -l 3 "$here/sample.pdf" "$tmp/scan"
img2pdf "$tmp/scan.png" -o "$here/scan.pdf"

echo "written: $here/sample.pdf $here/scan.pdf"
