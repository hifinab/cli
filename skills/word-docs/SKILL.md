---
name: word-docs
description: Read and write Word documents (.docx) with python-docx, including headings, paragraphs, bullet lists, tables, and images. Use when a task asks for a .docx report or gives .docx files to read.
---

# Word documents

## Read

```python
from docx import Document

doc = Document("input.docx")
for paragraph in doc.paragraphs:
    print(paragraph.style.name, "|", paragraph.text)
for table in doc.tables:
    for row in table.rows:
        print([cell.text for cell in row.cells])
```

Headers, footers, comments, and text boxes are not in `doc.paragraphs`; for
a quick full read, `pandoc input.docx -t markdown` (convert-docs) is often
easier.

## Write

```python
from docx import Document
from docx.shared import Pt, Cm

doc = Document()
doc.core_properties.title = "Nordic GPU providers"
doc.add_heading("Nordic GPU providers", level=0)
doc.add_paragraph("Five providers compared on price, hardware, and location.")
doc.add_heading("Summary", level=1)
for point in ["Cheapest H100: …", "Most locations: …"]:
    doc.add_paragraph(point, style="List Bullet")

rows = [("Provider", "H100 $/h", "Country"), ("A", "2.10", "SE")]
table = doc.add_table(rows=len(rows), cols=len(rows[0]), style="Light Grid Accent 1")
for r, row in enumerate(rows):
    for c, value in enumerate(row):
        table.cell(r, c).text = value

doc.add_picture("chart.png", width=Cm(15))
doc.save("report.docx")
```

- Use the built-in styles (`Heading 1`, `List Bullet`, `List Number`,
  table styles) rather than formatting runs by hand; the document then
  follows the reader's theme.
- Keep one idea per paragraph, and put sources in a final "Sources"
  heading.
- After saving, check the result: reopen it with `Document(...)` and count
  headings and tables, or convert it to markdown with pandoc and read it.
