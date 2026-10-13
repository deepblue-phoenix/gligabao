package memory_image

// LLM system prompt for the image pipeline (ExtractImage). The user message is the image itself.

const imageSystemPrompt = `You read an image: you transcribe its text (OCR) and say what kind of image it is.

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{
  "theme": "...",
  "title": "...",
  "text_blocks": [{"kind": "...", "text": "..."}]
}

Rules:
- "theme" classifies the WHOLE image as exactly one of:
  - "plot": a data plot with axes: line, scatter, bar or histogram plot of numeric data
  - "chart": a pie, donut, funnel, gantt or other non-axis chart of data
  - "graph": a node-and-edge graph or network (nodes connected by links)
  - "diagram": a flowchart, architecture, UML or other schematic diagram
  - "table": a grid of rows and columns
  - "document": a page of running text (article, letter, report, scanned page, form)
  - "screenshot": a screenshot of a user interface
  - "slide": a presentation slide
  - "map": a geographic map
  - "photo": a photograph of a real scene or object
  - "illustration": a drawing, logo, icon, cartoon or other artwork
  - "other": anything else
- "title" is the image's own title (the heading printed in it, e.g. a chart's title), copied exactly. Use "" if it has none. Do not invent a title.
- "text_blocks" lists every piece of text visible in the image, copied exactly as written (keep the original language, spelling and numbers; do not translate, correct or summarize), in natural reading order (top to bottom, left to right). A block is one coherent region: a heading, a paragraph, a caption, one axis label, one legend entry, one table row, ...
- "kind" of a block is the role of its text, one of: "title", "subtitle", "heading", "paragraph", "caption", "label" (a text label on a node, bar, slice or other drawn element), "axis" (an axis title or tick labels), "legend", "annotation" (a note, callout or footnote), "table" (a table row, cells separated by " | "), "other".
- If the title is in the image, also list it as the first block of kind "title".
- Only transcribe text that is really there. If the image has no text, return an empty "text_blocks".`
