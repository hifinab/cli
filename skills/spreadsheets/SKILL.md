---
name: spreadsheets
description: Read and write Excel workbooks (.xlsx) with openpyxl, including several sheets, formulas, number formats, column widths, and frozen header rows. Use when a task asks for an .xlsx file or gives one to read. For analysis, load the data with the tables skill.
---

# Excel workbooks

## Read

```python
from openpyxl import load_workbook

wb = load_workbook("input.xlsx", data_only=True)   # values, not formulas
for ws in wb.worksheets:
    print(ws.title, ws.dimensions)
    for row in ws.iter_rows(min_row=1, max_row=5, values_only=True):
        print(row)
```

`data_only=True` gives the values Excel last calculated; a file written by
a program that never ran Excel may have no cached values, so read it again
without it to see the formulas.

## Write

```python
from openpyxl import Workbook
from openpyxl.styles import Font
from openpyxl.utils import get_column_letter

wb = Workbook()
ws = wb.active
ws.title = "Prices"
ws.append(["Provider", "GPU", "USD per hour", "Source"])
ws.append(["A", "H100", 2.10, "https://…"])
for cell in ws[1]:
    cell.font = Font(bold=True)
ws.freeze_panes = "A2"
for row in ws.iter_rows(min_row=2, min_col=3, max_col=3):
    for cell in row:
        cell.number_format = "#,##0.00"
last = ws.max_row
ws.append(["Average", None, f"=AVERAGE(C2:C{last})"])
for column in range(1, ws.max_column + 1):
    width = max(len(str(c.value or "")) for c in ws[get_column_letter(column)])
    ws.column_dimensions[get_column_letter(column)].width = min(width + 2, 60)
wb.save("prices.xlsx")
```

- Store numbers as numbers and dates as dates, never as text, so the reader
  can sort and sum them.
- Write formulas for totals and averages the reader may change; openpyxl
  doesn't calculate them, so also say the values in your report.
- One table per sheet, starting at A1, with one header row.
