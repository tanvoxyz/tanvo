// Tanvo document template. Every text arrives translated and formatted in
// the JSON layout built by libs/documents; this file only lays it out, after
// lago-api's invoice, credit note and receipt stylesheets.
//
// Inputs (typst compile --input): dir is the job directory under the root,
// holding data.json and the document's other files; facturx, when set,
// names the Factur-X XML to attach.

#let dir = sys.inputs.at("dir")
#let data = json(dir + "/data.json")

#let ink = rgb("#19212e")
#let muted = rgb("#66758f")
#let rule = rgb("#d9dee7")
#let alert-fill = rgb("#f3f4f6")

// lago-api sizes are CSS pixels (0.75pt).
#let body-size = 7.5pt
#let small-size = 6.75pt
#let line-gap = 4.5pt
#let block-gap = 18pt

#set document(title: data.title)
// The font fallback chain comes with the data (fontChain in
// libs/documents/fonts.go): Inter, then Noto Sans CJK SC/TC for zh-CN and
// zh-TW.
#set text(
  font: data.fonts,
  size: body-size,
  fill: ink,
  lang: data.lang,
  region: data.at("region", default: none),
)
#set par(leading: line-gap, spacing: line-gap)

#let ft = data.footer
#set page(
  paper: "a4",
  margin: (x: 1.5cm, top: 1.5cm, bottom: 1.8cm),
  footer: context {
    set text(size: small-size, fill: muted)
    let parts = ft.page.map(p => {
      let c = p.at("counter", default: none)
      if c == "current" { str(counter(page).get().first()) } else if c == "total" {
        str(counter(page).final().first())
      } else { p.at("text", default: "") }
    })
    grid(
      columns: (1fr, auto),
      ft.number, parts.join(),
    )
  },
)

#if "facturx" in sys.inputs {
  pdf.attach(
    "/factur-x.xml",
    read(dir + "/" + sys.inputs.at("facturx"), encoding: none),
    relationship: "alternative",
    mime-type: "text/xml",
    description: "Factur-X/ZUGFeRD invoice",
  )
}

#let body1(s) = text(weight: "bold", s)
#let body3(s) = text(size: small-size, fill: muted, s)
#let lines(xs, style) = stack(spacing: line-gap, ..xs.map(style))

// Header: title and logo.
#grid(
  columns: (1fr, auto),
  align: (left + horizon, right + horizon),
  text(size: 18pt, weight: "bold", data.title),
  if "logo" in data { image(dir + "/" + data.logo, height: 24pt) },
)
#v(block-gap)

// Document information and customer metadata.
#let info-table(rows) = grid(
  columns: (auto, 1fr),
  column-gutter: 12pt,
  row-gutter: line-gap,
  ..rows.map(r => (body1(r.at(0)), r.at(1))).flatten(),
)
#grid(
  columns: (1fr, 1fr),
  column-gutter: 12pt,
  info-table(data.info), info-table(data.meta),
)
#v(block-gap)

// Parties.
#grid(
  columns: (1fr, 1fr),
  column-gutter: 12pt,
  ..data.parties.map(p => stack(spacing: line-gap, body1(p.title), ..p.lines.map(l => l))),
)
#v(block-gap)

// Headline amount.
#text(size: 13.5pt, weight: "bold", data.amount)
#if data.at("amount_note", default: "") != "" {
  v(3pt)
  body1(data.amount_note)
}
#v(block-gap)

#let align-of(a) = if a == "right" { right } else { left }

#let render-cell(c, style, presentation) = {
  let main = c.at("text", default: ())
  let sub = c.at("sub", default: ())
  let strong = c.at("bold", default: false)
  let color = if strong { ink } else if style in ("head", "detail") or (presentation and style in ("plain", "end")) {
    muted
  } else { ink }
  stack(
    spacing: 3pt,
    ..main.map(t => text(weight: if strong { "bold" } else { "regular" }, fill: color, t)),
    ..sub.map(t => text(size: small-size, fill: muted, t)),
  )
}

#let item-table(b) = {
  let cols = b.columns
  let rows = b.rows
  let tstyle = b.at("style", default: "")
  let presentation = tstyle == "presentation"
  let breakdown = tstyle == "breakdown"
  let cells = ()
  for (i, r) in rows.enumerate() {
    let s = r.style
    let next = if i + 1 < rows.len() { rows.at(i + 1).style } else { none }
    if breakdown {
      cells.push(table.hline(stroke: 0.75pt + rule))
    } else if s in ("fee", "name") and i > 0 and not presentation {
      cells.push(table.hline(stroke: 0.75pt + rule))
    }
    let top = if s in ("fee", "name") { 6pt } else if s == "head" { 3pt } else if breakdown { 6pt } else { 3pt }
    let bottom = if s == "name" { 0pt } else if next in ("fee", "name", none) and not presentation { 6pt } else { 3pt }
    for (j, c) in r.cells.enumerate() {
      let indent = if j == 0 and s in ("detail", "subtotal") { 6pt } else if j == 0 and presentation and s in ("plain", "end") { 6pt } else { 0pt }
      cells.push(pad(top: top, bottom: bottom, left: indent, render-cell(c, s, presentation)))
    }
    if presentation and (s == "end" or s == "head") {
      cells.push(table.hline(stroke: 0.75pt + rule))
    }
  }
  if not presentation { cells.push(table.hline(stroke: 0.75pt + rule)) }
  table(
    columns: cols.map(c => c.width * 1fr),
    align: cols.map(c => align-of(c.align)),
    stroke: none,
    inset: (x: 0pt, y: 0pt),
    column-gutter: 6pt,
    ..cells,
  )
}

#let totals-table(rows) = {
  let cells = ()
  for (i, r) in rows.enumerate() {
    let strong = r.at("strong", default: false)
    let style(s) = if strong { body1(s) } else { s }
    cells.push([])
    cells.push(pad(y: 6pt, style(r.label)))
    cells.push(pad(y: 6pt, style(r.value)))
    if not strong and i + 1 < rows.len() {
      cells.push(table.hline(start: 1, stroke: 0.75pt + rule))
    }
  }
  table(
    columns: (50%, 35%, 15%),
    align: (left, left, right),
    stroke: none,
    inset: (x: 0pt, y: 0pt),
    column-gutter: 6pt,
    ..cells,
  )
}

#for b in data.blocks {
  if b.at("break", default: false) { pagebreak(weak: true) }
  if b.at("gap", default: false) { v(block-gap) }
  let k = b.kind
  if k == "heading" {
    block(below: block-gap, text(size: 13.5pt, weight: "bold", b.text))
  } else if k == "table" {
    block(below: if b.at("style", default: "") == "resume" { 0pt } else { block-gap }, item-table(b))
  } else if k == "totals" {
    block(below: block-gap, totals-table(b.totals))
  } else if k == "text" {
    let st = b.at("style", default: "body3")
    if st == "alert" {
      block(below: block-gap, width: 100%, fill: alert-fill, radius: 9pt, inset: 12pt, lines(b.lines, body3))
    } else if st == "body1" {
      block(below: block-gap, lines(b.lines, body1))
    } else {
      block(below: block-gap, lines(b.lines, body3))
    }
  } else if k == "sections" {
    for s in b.sections {
      block(above: block-gap, below: 0pt, stroke: (bottom: 0.75pt + rule), inset: (bottom: block-gap), width: 100%, stack(
        spacing: 6pt,
        body1(s.title),
        lines(s.lines, body3),
      ))
    }
    v(block-gap)
  }
}
