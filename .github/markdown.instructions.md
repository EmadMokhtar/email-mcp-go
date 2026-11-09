---
description: 'Documentation and content creation standards'
applyTo: 'site/content/**/*.md'
---

## Markdown Content Rules

These rules describe the pages of the docs site in `site/content/`. Hugo
builds the site, and it shows the page title from the front matter, so a page
has no H1. The validators are the programs that check the pages when
`make site` runs: the site check (`test/sitecheck`), and the Hugo template
`site/layouts/_markup/render-link.html`, which draws each link and stops the
build on a broken internal link. They check some of the rules (see the list
below).
The rules do not apply to the other Markdown files of the repository:
`README.md` opens with an H1 and carries no front matter, which is correct for
a file read directly on GitHub, and `docs/superpowers/` holds the plans and
specs, which are not published.

These rules are checked by the validators: H1, depth, code fence language,
line length, links, images, tables and `code_areas`. The other rules are
guidance. A check covers only the part that its rule names:

1. **Headings**: Use appropriate heading levels (H2, H3, etc.) to structure your content. Do not use an H1 heading, as this will be generated based on the title. Checked: no H1, and no heading deeper than H3.
2. **Lists**: Use bullet points or numbered lists for lists. Ensure proper indentation and spacing.
3. **Code Blocks**: Use fenced code blocks for code snippets. Specify the language for syntax highlighting. Checked: every code fence has a language.
4. **Links**: Use proper markdown syntax for links. Ensure that links are valid and accessible. Checked: every link inside the site goes to a page and an anchor that exist, and every link to the source code on GitHub goes to a path that exists. A link that starts with `/` must go to a page of the site. Links to other sites are not checked.
5. **Images**: Use proper markdown syntax for images. Include alt text for accessibility. Checked: every image has alt text, and its file exists. An image that loads from another site fails the build.
6. **Tables**: Use markdown tables for tabular data. Ensure proper formatting and alignment. Checked: every table has a name (`aria-label`, the attribute that a screen reader reads as the name of the table) that no other table of the page has, and sits in a scroll box (a box that scrolls sideways when the table is too wide). The table hook (`site/layouts/_markup/render-table.html`, the template that draws each Markdown table) writes both. The formatting and the alignment are not checked.
7. **Line Length**: Limit line length to 80 characters, except in tables and code blocks. Checked, outside the front matter, the code blocks and the tables.
8. **Whitespace**: Use appropriate whitespace to separate sections and improve readability.
9. **Front Matter**: Include YAML front matter at the beginning of the file with required metadata fields. Checked: only `code_areas` (see below). `title`, `description` and `weight` are not checked.

## Formatting and Structure

Follow these guidelines for formatting and structuring your markdown content:

- **Headings**: Use `##` for H2 and `###` for H3. Ensure that headings are used in a hierarchical manner. Do not use H4 or deeper: `make site` fails.
- **Lists**: Use `-` for bullet points and `1.` for numbered lists. Indent nested lists with two spaces.
- **Code Blocks**: Use triple backticks (`) to create fenced code blocks. Specify the language after the opening backticks for syntax highlighting (e.g., `csharp).
- **Links**: Use `[link text](URL)` for links. Ensure that the link text is descriptive and the URL is valid.
- **Images**: Use `![alt text](image URL)` for images. Include a brief description of the image in the alt text.
- **Tables**: Use `|` to create tables. Ensure that columns are properly aligned and headers are included.
- **Line Length**: Break lines at 80 characters to improve readability. Use soft line breaks for long paragraphs.
- **Whitespace**: Use blank lines to separate sections and improve readability. Avoid excessive whitespace.

## Validation Requirements

Ensure compliance with the following validation requirements:

- **Front Matter**: Include the following fields in the YAML front matter:

  - `title`: The title of the page.
  - `description`: One sentence about the page, for search engines and link previews.
  - `weight`: The place of the page in the sidebar. A lower number comes first.
  - `code_areas`: Manual and tech docs pages only. The repository paths that the page covers. The build fails when a path does not exist, and when a regular page of the manual or the tech docs has none. A regular page is a page that is not the `_index.md` of a section. The glossaries need none.
  - `aliases`: Optional. The old addresses of a page that moved.

  The landing page (`site/content/_index.md`) has more fields, which its template reads.

- **Content Rules**: Ensure that the content follows the markdown content rules specified above.
- **Formatting**: Ensure that the content is properly formatted and structured according to the guidelines.
- **Validation**: Run the validation tools to check for compliance with the rules and guidelines.
