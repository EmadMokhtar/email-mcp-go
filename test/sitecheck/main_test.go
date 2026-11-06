package main

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// subPath is where GitHub Pages serves the site. The root tests use "/".
const subPath = "/email-mcp-go/"

// regularPage is the page that newSite adds to every test site. The real site
// always has a regular page, and the check fails a site that has none.
const regularPage = "site/public/regular/index.html"

// newSite writes a small repository in a temporary folder and returns the
// config that points at it. The keys of files are paths from the root of the
// repository, so the built pages are under site/public/.
func newSite(t *testing.T, base string, files map[string]string) config {
	t.Helper()
	root := t.TempDir()
	// The real site always has a content folder. The check reports its absence,
	// so every test site has one.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "site", "content"), 0o750))
	// A test that needs a site with no regular page removes this file.
	files = maps.Clone(files)
	if files == nil {
		files = map[string]string{}
	}
	files[regularPage] = htmlPageKind("page", "", "")
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	return config{Public: filepath.Join(root, "site", "public"), Base: base, Root: root}
}

// htmlPage returns a page with extra markup in the head and in the body.
func htmlPage(head, body string) string {
	return htmlPageKind("", head, body)
}

// htmlPageKind is htmlPage with a data-kind on the body, like the page shell.
func htmlPageKind(kind, head, body string) string {
	attr := ""
	if kind != "" {
		attr = ` data-kind="` + kind + `"`
	}
	return "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<title>T</title>\n" + head + "\n</head>\n<body" + attr + ">\n" + body +
		"\n</body>\n</html>\n"
}

// requireClean checks that the site has no problem.
func requireClean(t *testing.T, cfg config) {
	t.Helper()
	require.Empty(t, check(cfg))
}

// requireOneProblem checks that the site has exactly one problem, in the file at
// path (from the root of the repository), and that its text contains want.
func requireOneProblem(t *testing.T, cfg config, path, want string) {
	t.Helper()
	got := check(cfg)
	require.Len(t, got, 1, "problems: %v", got)
	require.Equal(t, filepath.Join(cfg.Root, filepath.FromSlash(path)), got[0].File)
	require.Contains(t, got[0].Text, want)
}

// requireOutcome runs requireClean when want is empty, and requireOneProblem
// otherwise. It lets one table hold both the passing and the failing cases.
func requireOutcome(t *testing.T, cfg config, path, want string) {
	t.Helper()
	if want == "" {
		requireClean(t, cfg)
		return
	}
	requireOneProblem(t, cfg, path, want)
}

func TestRootLinkFailsUnderBasePath(t *testing.T) {
	cfg := newSite(t, subPath, map[string]string{
		"site/public/index.html":        htmlPage("", `<a href="/manual/">Manual</a>`),
		"site/public/manual/index.html": htmlPage("", ""),
	})
	requireOneProblem(t, cfg, "site/public/index.html",
		`"/manual/" goes outside the base path /email-mcp-go/`)
}

func TestCheckPassesAtRoot(t *testing.T) {
	cfg := newSite(t, "/", map[string]string{
		"site/public/index.html":        htmlPage("", `<a href="/manual/">Manual</a>`),
		"site/public/manual/index.html": htmlPage("", ""),
	})
	requireClean(t, cfg)
}

// TestLinksThatPass covers the ways to write a link that is good. All of them
// are in a page two levels below the base path.
func TestLinksThatPass(t *testing.T) {
	for _, href := range []string{
		"/email-mcp-go/manual/",
		"/email-mcp-go/manual",
		"/email-mcp-go/",
		"../",
		"./",
		"",
		"#",
		"#steps",
		"/email-mcp-go/manual/install/#steps",
		"/email-mcp-go/style.css?v=1",
		"mailto:me@example.com",
		"https://example.com/anywhere",
	} {
		t.Run(href, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html":                htmlPage("", ""),
				"site/public/style.css":                 "",
				"site/public/manual/index.html":         htmlPage("", ""),
				"site/public/manual/install/index.html": htmlPage("", `<h2 id="steps">Steps</h2><a href="`+href+`">x</a>`),
			})
			requireClean(t, cfg)
		})
	}
}

func TestBrokenLinkFails(t *testing.T) {
	for _, href := range []string{
		"/email-mcp-go/manual/missing/",
		"/email-mcp-go/missing.html",
		"../missing/",
		"/email-mcp-go/index.html/",
	} {
		t.Run(href, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html":                htmlPage("", ""),
				"site/public/manual/install/index.html": htmlPage("", `<a href="`+href+`">x</a>`),
			})
			requireOneProblem(t, cfg, "site/public/manual/install/index.html", "goes to a missing file")
		})
	}
}

func TestBrokenAnchorFails(t *testing.T) {
	for _, href := range []string{
		"/email-mcp-go/manual/#nope",
		"#nope",
	} {
		t.Run(href, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html":        htmlPage("", `<a href="`+href+`">x</a>`),
				"site/public/manual/index.html": htmlPage("", `<h2 id="steps">Steps</h2>`),
			})
			requireOneProblem(t, cfg, "site/public/index.html", `has no element with id "nope"`)
		})
	}
}

func TestOutsideResourceFails(t *testing.T) {
	for name, markup := range map[string]struct{ head, body string }{
		"stylesheet":           {head: `<link rel="stylesheet" href="https://x.test/a.css">`},
		"stylesheet no scheme": {head: `<link rel="stylesheet" href="//x.test/a.css">`},
		"icon":                 {head: `<link rel="icon" href="https://x.test/i.svg">`},
		"shortcut icon":        {head: `<link rel="shortcut icon" href="https://x.test/i.ico">`},
		"image":                {body: `<img src="https://x.test/a.png" alt="A">`},
		"image srcset": {
			body: `<img src="/email-mcp-go/a.png" srcset="/email-mcp-go/a.png 1x, https://x.test/b.png 2x" alt="A">`,
		},
		"picture source": {
			body: `<picture><source srcset="https://x.test/a.png"><img src="/email-mcp-go/a.png" alt="A"></picture>`,
		},
		"media source": {body: `<video><source src="https://x.test/a.mp4"></video>`},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html": htmlPage(markup.head, markup.body),
				"site/public/a.png":      "",
			})
			requireOneProblem(t, cfg, "site/public/index.html", "loads from another site")
		})
	}
}

func TestCanonicalLinkPasses(t *testing.T) {
	cfg := newSite(t, subPath, map[string]string{
		"site/public/index.html": htmlPage(`<link rel="canonical" href="https://x.test/">`, ""),
	})
	requireClean(t, cfg)
}

func TestMissingIconFails(t *testing.T) {
	head := `<link rel="icon" type="image/svg+xml" href="/email-mcp-go/icon.svg">`
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "no icon file",
			files: map[string]string{"site/public/index.html": htmlPage(head, "")},
			want:  "goes to a missing file",
		},
		{
			name: "icon file",
			files: map[string]string{
				"site/public/index.html": htmlPage(head, ""),
				"site/public/icon.svg":   "<svg/>",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireOutcome(t, newSite(t, subPath, tc.files), "site/public/index.html", tc.want)
		})
	}
}

func TestSocialImageMustExist(t *testing.T) {
	for _, tc := range []struct {
		name    string
		attr    string // the attribute that holds the property name
		content string
		files   map[string]string
		want    string
	}{
		{
			name:    "no file",
			attr:    "property",
			content: "https://x.test/email-mcp-go/social.png",
			want:    "goes to a missing file",
		},
		{
			name:    "file under the base path",
			attr:    "property",
			content: "https://x.test/email-mcp-go/social.png",
			files:   map[string]string{"site/public/social.png": ""},
		},
		{
			name:    "address with another base path",
			attr:    "property",
			content: "https://x.test/social.png",
			files:   map[string]string{"site/public/social.png": ""},
			want:    "outside the base path",
		},
		{
			name:    "name attribute",
			attr:    "name",
			content: "https://x.test/email-mcp-go/social.png",
			want:    "goes to a missing file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := `<meta ` + tc.attr + `="og:image" content="` + tc.content + `">`
			files := map[string]string{"site/public/index.html": htmlPage(meta, "")}
			for name, body := range tc.files {
				files[name] = body
			}
			requireOutcome(t, newSite(t, subPath, files), "site/public/index.html", tc.want)
		})
	}
}

func TestScriptFails(t *testing.T) {
	for name, body := range map[string]string{
		"inline":   `<script>alert(1)</script>`,
		"external": `<script src="/email-mcp-go/a.js"></script>`,
		"data":     `<script type="application/ld+json">{}</script>`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html": htmlPage("", body),
				"site/public/a.js":       "",
			})
			requireOneProblem(t, cfg, "site/public/index.html", "the site has no script")
		})
	}
}

func TestImageWithoutAltFails(t *testing.T) {
	for _, tc := range []struct{ name, img, want string }{
		{"no alt", `<img src="/email-mcp-go/a.png">`, "has no alt text"},
		{"empty alt", `<img src="/email-mcp-go/a.png" alt="">`, "has no alt text"},
		{"blank alt", `<img src="/email-mcp-go/a.png" alt="  ">`, "has no alt text"},
		{"alt text", `<img src="/email-mcp-go/a.png" alt="A sign-in page">`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html": htmlPage("", tc.img),
				"site/public/a.png":      "",
			})
			requireOutcome(t, cfg, "site/public/index.html", tc.want)
		})
	}
}

func TestUnusedScreenshotFails(t *testing.T) {
	page := htmlPage("", "")
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name: "one of two is used",
			files: map[string]string{
				"site/assets/screenshots/waiting-light.png": "",
				"site/assets/screenshots/waiting-dark.png":  "",
				"site/public/screenshots/waiting-dark.png":  "",
			},
			want: "no page uses this screenshot",
		},
		{
			name: "both are used",
			files: map[string]string{
				"site/assets/screenshots/waiting-light.png": "",
				"site/assets/screenshots/waiting-dark.png":  "",
				"site/public/screenshots/waiting-light.png": "",
				"site/public/screenshots/waiting-dark.png":  "",
			},
		},
		{
			name:  "a file that is not an image is ignored",
			files: map[string]string{"site/assets/screenshots/.done": ""},
		},
		{
			name:  "no screenshots folder is no images",
			files: map[string]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.files["site/public/index.html"] = page
			requireOutcome(t, newSite(t, subPath, tc.files),
				"site/assets/screenshots/waiting-light.png", tc.want)
		})
	}
}

func TestCodeAreaPathMustExist(t *testing.T) {
	for _, tc := range []struct{ name, path, want string }{
		{"file", "internal/outbox/outbox.go", ""},
		{"folder", "internal/outbox/", ""},
		{"missing path", "internal/outbox/gone.go", "does not exist"},
		{"empty", "", "not a path inside the repository"},
		{"outside the repository", "../secret", "not a path inside the repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html":    htmlPage(`<meta name="code-area" content="`+tc.path+`">`, ""),
				"internal/outbox/outbox.go": "package outbox\n",
			})
			requireOutcome(t, cfg, "site/public/index.html", tc.want)
		})
	}
}

func TestRegularPageNeedsCodeArea(t *testing.T) {
	const area = `<meta name="code-area" content="internal/outbox/outbox.go">`
	for _, tc := range []struct {
		name string
		page string // path of the page below site/public
		kind string
		head string
		want string
	}{
		{"manual page with no area", "manual/send/index.html", "page", "", "no code-area"},
		{"tech page with no area", "tech/outbox/index.html", "page", "", "no code-area"},
		{"tech page with an area", "tech/outbox/index.html", "page", area, ""},
		{"manual glossary", "manual/glossary/index.html", "page", "", ""},
		{"tech glossary", "tech/glossary/index.html", "page", "", ""},
		{"name that only ends like the glossary", "tech/myglossary/index.html", "page", "", "no code-area"},
		{"page outside the manual and the tech docs", "about/index.html", "page", "", ""},
		{"section page", "manual/index.html", "section", "", ""},
		{"page with no kind", "manual/send/index.html", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html":    htmlPage("", ""),
				"site/public/" + tc.page:    htmlPageKind(tc.kind, tc.head, ""),
				"internal/outbox/outbox.go": "package outbox\n",
			})
			requireOutcome(t, cfg, "site/public/"+tc.page, tc.want)
		})
	}
}

// TestSiteWithNoRegularPageFails guards the code-area rule. The rule only runs
// on pages with data-kind="page". If the page shell loses that marker, no page
// would be checked, and the rule would pass with no sign. So the check fails a
// built site that has no regular page.
func TestSiteWithNoRegularPageFails(t *testing.T) {
	for _, tc := range []struct {
		name, kind, want string
	}{
		{"markers of other kinds only", "section", `no page has data-kind="page"`},
		{"no marker at all", "", `no page has data-kind="page"`},
		{"one regular page among other kinds", "page", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html":        htmlPageKind("home", "", ""),
				"site/public/manual/index.html": htmlPageKind("section", "", ""),
				"site/public/about/index.html":  htmlPageKind(tc.kind, "", ""),
			})
			// Take away the page that newSite adds, so that the pages of this
			// test are the only ones.
			require.NoError(t, os.Remove(filepath.Join(cfg.Root, filepath.FromSlash(regularPage))))
			requireOutcome(t, cfg, "site/public", tc.want)
		})
	}
}

func TestSourceLinkPathMustExist(t *testing.T) {
	for _, tc := range []struct{ name, href, want string }{
		{"file", repoURL + "/blob/main/internal/outbox/outbox.go", ""},
		{"file with a line", repoURL + "/blob/v0.6.0/internal/outbox/outbox.go#L10", ""},
		{"folder", repoURL + "/tree/main/internal/outbox/", ""},
		{"missing file", repoURL + "/blob/main/internal/outbox/gone.go", "does not exist"},
		{"missing folder", repoURL + "/tree/abc123/internal/gone/", "does not exist"},
		{"path that leaves the repository", repoURL + "/blob/main/../../etc/passwd", "leaves the repository"},
		{"repository address", repoURL, ""},
		{"repository root", repoURL + "/tree/main", ""},
		{"other page of the repository", repoURL + "/issues/3", ""},
		{"other repository", "https://github.com/other/project/blob/main/gone.go", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html":    htmlPage("", `<a href="`+tc.href+`">x</a>`),
				"internal/outbox/outbox.go": "package outbox\n",
			})
			requireOutcome(t, cfg, "site/public/index.html", tc.want)
		})
	}
}

func TestContentRules(t *testing.T) {
	line80 := strings.Repeat("a", 80)
	line81 := strings.Repeat("a", 81)
	for _, tc := range []struct{ name, md, want string }{
		{"H2 and H3", "## A\n\n### B\n", ""},
		{"H1 in the body", "---\ntitle: T\n---\n# Title\n", "line 4: H1"},
		{"H4", "## A\n\n#### Deep\n", "line 3: heading deeper than H3"},
		{"H5 with spaces before it", "   ##### Deep\n", "line 1: heading deeper than H3"},
		{"hash with no space is text", "#T13 and #hashtag\n", ""},
		{"too many hashes is text", "####### text\n", ""},
		{"80 characters", line80 + "\n", ""},
		{"81 characters", line81 + "\n", "line 1: 81 characters"},
		{"80 characters that take more bytes", strings.Repeat("é", 80) + "\n", ""},
		{"81 characters that take more bytes", strings.Repeat("é", 81) + "\n", "line 1: 81 characters"},
		{"line ends with a carriage return", line80 + "\r\n", ""},
		{"front matter is skipped", "---\ntitle: T\n# T13\ndescription: " + line81 + "\n---\n\n## A\n", ""},
		{"TOML front matter is skipped", "+++\n# T13\ndescription = \"" + line81 + "\"\n+++\n\n## A\n", ""},
		{"text after the front matter is checked", "---\ntitle: T\n---\n" + line81 + "\n", "line 4: 81 characters"},
		{"code fence is skipped", "```sh\n# comment\n#### x\n" + line81 + "\n```\n", ""},
		{"code fence with tildes is skipped", "~~~sh\n# comment\n" + line81 + "\n~~~\n", ""},
		{"long fence ends with a long fence", "````md\n```\n# x\n```\n" + line81 + "\n````\n", ""},
		{"fence inside a list is skipped", "1. Run:\n\n   ```sh\n   # comment\n   " + line81 + "\n   ```\n", ""},
		{"text after the code fence is checked", "```sh\nx\n```\n# Title\n", "line 4: H1"},
		{"table is skipped", "| a | " + line81 + " |\n| - | - |\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newSite(t, subPath, map[string]string{
				"site/public/index.html":           htmlPage("", ""),
				"site/content/manual/page.md":      tc.md,
				"site/content/manual/not-a-page.x": "# not Markdown\n",
			})
			requireOutcome(t, cfg, "site/content/manual/page.md", tc.want)
		})
	}
}

// TestPlainFailures covers the cases where the site cannot be read at all. They
// must show as problems, because a check that finds nothing to read would pass.
func TestPlainFailures(t *testing.T) {
	t.Run("no built site", func(t *testing.T) {
		cfg := newSite(t, subPath, nil)
		require.NoError(t, os.RemoveAll(cfg.Public))
		got := check(cfg)
		require.Len(t, got, 1, "problems: %v", got)
		require.Equal(t, cfg.Public, got[0].File)
		require.Contains(t, got[0].Text, "cannot read the built site")
	})
	t.Run("a built site with no page", func(t *testing.T) {
		cfg := newSite(t, subPath, map[string]string{"site/public/style.css": ""})
		require.NoError(t, os.Remove(filepath.Join(cfg.Root, filepath.FromSlash(regularPage))))
		got := check(cfg)
		require.Len(t, got, 1, "problems: %v", got)
		require.Contains(t, got[0].Text, "no HTML page")
	})
	t.Run("no content folder", func(t *testing.T) {
		cfg := newSite(t, subPath, map[string]string{"site/public/index.html": htmlPage("", "")})
		require.NoError(t, os.Remove(filepath.Join(cfg.Root, "site", "content")))
		got := check(cfg)
		require.Len(t, got, 1, "problems: %v", got)
		require.Contains(t, got[0].Text, "cannot read the content")
	})
	t.Run("base path with no slash at the end", func(t *testing.T) {
		cfg := newSite(t, "/email-mcp-go", map[string]string{
			"site/public/index.html":        htmlPage("", `<a href="/email-mcp-go/manual/">x</a><a href="/email-mcp-gofoo/">y</a>`),
			"site/public/manual/index.html": htmlPage("", ""),
		})
		requireOneProblem(t, cfg, "site/public/index.html", `"/email-mcp-gofoo/" goes outside the base path`)
	})
}

func TestRun(t *testing.T) {
	good := newSite(t, subPath, map[string]string{"site/public/index.html": htmlPage("", "")})
	bad := newSite(t, subPath, map[string]string{
		"site/public/index.html": htmlPage("", `<script></script><a href="/gone/">x</a>`),
	})
	args := func(cfg config) []string {
		return []string{"-public", cfg.Public, "-base", cfg.Base, "-root", cfg.Root}
	}

	var out strings.Builder
	require.Equal(t, 0, run(args(good), &out))
	require.Empty(t, out.String(), "a good site prints nothing")

	out.Reset()
	require.Equal(t, 1, run(args(bad), &out))
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 2, "one line for each problem: %q", out.String())
	for _, line := range lines {
		require.True(t, strings.HasPrefix(line, filepath.Join(bad.Public, "index.html")+": "), line)
	}

	out.Reset()
	require.Equal(t, 2, run([]string{"-public", good.Public}, &out))
	require.Contains(t, out.String(), "-base is required")

	out.Reset()
	require.Equal(t, 2, run([]string{"-nope"}, &out))
}
