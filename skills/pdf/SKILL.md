---
name: pdf
description: Use this skill whenever PDF files are created, read, merged, split, transformed, OCR-processed, or data-extracted.
---

# PDF Skill (Anthropic Official Source)

Source: `https://github.com/anthropics/skills/tree/main/skills/pdf`

Use this skill whenever PDF artifacts are involved.

## Typical Triggers

- "extract text/tables from this PDF"
- "merge or split PDFs"
- "rotate or watermark pages"
- "OCR this scanned document"

## Working Rules

1. Prefer reproducible scripted operations.
2. Preserve page order and metadata unless explicitly changed.
3. Validate output page count and extraction quality after transforms.
4. Record command-level provenance for manuscript claims.
5. When uploading a PDF with Gitea MCP `create_or_update_file`, base64-encode the raw PDF bytes (without a `data:application/pdf` prefix) and pass the string as `content` with `encoding: "base64"`. Do not send PDF bytes as UTF-8 text.

## Quick Commands

```python
from pypdf import PdfReader
reader = PdfReader("document.pdf")
print(len(reader.pages))
```
