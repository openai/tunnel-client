package assistantkb

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
)

//go:embed *.md deployment/*.md
var knowledgeFS embed.FS

type knowledgeDocument struct {
	Path       string
	PathLower  string
	Title      string
	TitleLower string
	Sections   []knowledgeSection
}

type knowledgeSection struct {
	Heading      string
	HeadingLower string
	Body         string
	SearchText   string
}

type Match struct {
	Path    string
	Heading string
	Excerpt string
	Score   int
}

var (
	knowledgeDocsOnce sync.Once
	knowledgeDocs     []knowledgeDocument
	knowledgeDocsErr  error

	knowledgeTokenRE   = regexp.MustCompile(`[a-z0-9_./-]+`)
	knowledgeStopwords = map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
		"do": {}, "for": {}, "from": {}, "get": {}, "how": {}, "i": {}, "in": {}, "into": {},
		"is": {}, "it": {}, "its": {}, "of": {}, "on": {}, "or": {}, "should": {}, "that": {},
		"the": {}, "their": {}, "them": {}, "this": {}, "to": {}, "use": {}, "what": {}, "when": {},
		"where": {}, "which": {}, "with": {}, "your": {},
	}
	knowledgeShortAllowlist = map[string]struct{}{
		"id": {}, "ui": {}, "mcp": {},
	}
)

func BuildPromptContext(prompt string) string {
	if text := buildMissingBinaryPromptContext(prompt); text != "" {
		return text
	}
	matches := Search(prompt, 3)
	return FormatPromptContext([]string{
		"Packaged tunnel-client knowledge base injected from the binary.",
		"These markdown snippets remain available even when the source checkout is not present on disk.",
		"Use them as repository-specific reference before falling back to generic assumptions.",
	}, "knowledge.match", matches)
}

func FormatPromptContext(intro []string, matchPrefix string, matches []Match) string {
	if len(matches) == 0 {
		return ""
	}
	if strings.TrimSpace(matchPrefix) == "" {
		matchPrefix = "knowledge.match"
	}
	lines := append([]string{}, intro...)
	for i, match := range matches {
		index := i + 1
		lines = append(lines, fmt.Sprintf("%s.%d.path=%s", matchPrefix, index, match.Path))
		if match.Heading != "" {
			lines = append(lines, fmt.Sprintf("%s.%d.heading=%s", matchPrefix, index, match.Heading))
		}
		lines = append(lines,
			fmt.Sprintf("%s.%d.excerpt_begin", matchPrefix, index),
			match.Excerpt,
			fmt.Sprintf("%s.%d.excerpt_end", matchPrefix, index),
		)
	}
	return strings.Join(lines, "\n")
}

func buildMissingBinaryPromptContext(prompt string) string {
	return buildMissingBinaryPromptContextForOS(prompt, runtime.GOOS)
}

func buildMissingBinaryPromptContextForOS(prompt string, goos string) string {
	if !isMissingBinaryPrompt(prompt) {
		return ""
	}
	buildCommand, wrapperCommand, binaryFlag := BinaryAcquisitionGuidanceForOS(goos)
	lines := []string{
		"Deterministic tunnel-client binary-missing guidance injected from the binary.",
		"This path is intentionally not retrieval-based because missing-binary prompts must stay public-safe and binary-first.",
		"Use these exact public-safe anchors in the answer:",
		"https://github.com/openai/tunnel-client/releases/latest",
		"https://github.com/openai/tunnel-client",
		"git clone https://github.com/openai/tunnel-client.git",
		buildCommand,
		"After the binary exists, install or refresh the plugin with:",
		"tunnel-client codex plugin install",
		"If the user is working from an exported plugin bundle or source checkout, the wrapper-first fallback command for this OS is:",
		wrapperCommand,
		"You can also point the plugin at the binary directly with:",
		"TUNNEL_CLIENT_BIN",
		binaryFlag,
		"Do not suggest non-public installer or checkout-specific commands for generic missing-binary guidance.",
	}
	return strings.Join(lines, "\n")
}

func BinaryAcquisitionGuidanceForOS(goos string) (buildCommand string, wrapperCommand string, binaryFlag string) {
	switch goos {
	case "windows":
		return "go build -o bin/tunnel-client.exe ./cmd/client",
			`powershell -NoProfile -ExecutionPolicy Bypass -File .\\scripts\\Install-Plugin.ps1 --tunnel-client-bin C:\\path\\to\\tunnel-client.exe`,
			`--tunnel-client-bin C:\\path\\to\\tunnel-client.exe`
	default:
		return "go build -o bin/tunnel-client ./cmd/client",
			"sh scripts/install_plugin.sh --tunnel-client-bin /path/to/tunnel-client",
			"--tunnel-client-bin /path/to/tunnel-client"
	}
}

func isMissingBinaryPrompt(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return false
	}
	if !containsKnowledgeAny(lower, "tunnel-client", "tunnel client", "tunnel-mcp", "tunnel mcp") {
		return false
	}

	hasMissingSignal := containsKnowledgeAny(lower,
		"missing",
		"not found",
		"can't find",
		"cannot find",
		"could not find",
		"can't locate",
		"cannot locate",
		"could not locate",
		"no such file or directory",
		"command not found",
		"not installed",
		"not on path",
		"download",
		"get a binary",
		"obtain a binary",
	)
	hasBinarySubject := containsKnowledgeAny(lower,
		"binary",
		"executable",
		"plugin",
		"path",
		"on path",
		"command",
		"command -v",
		"download",
		"get a binary",
		"obtain a binary",
	)
	if hasMissingSignal && hasBinarySubject {
		return true
	}

	if containsKnowledgeAny(lower, "install tunnel-client", "install the tunnel-client", "set up tunnel-client", "setup tunnel-client", "build tunnel-client") &&
		containsKnowledgeAny(lower, "binary", "executable", "from source", "public repo", "github") {
		return true
	}

	if containsKnowledgeAny(lower, "download tunnel-client", "download the tunnel-client") {
		return true
	}

	if !containsKnowledgeAny(lower, "install", "download") {
		return false
	}
	return containsKnowledgeAny(lower, "binary", "executable")
}

func containsKnowledgeAny(text string, needles ...string) bool {
	return slices.ContainsFunc(needles, func(needle string) bool {
		return strings.Contains(text, needle)
	})
}

func Search(prompt string, limit int) []Match {
	if limit <= 0 {
		limit = 1
	}
	docs, err := loadKnowledgeDocuments()
	if err != nil || len(docs) == 0 {
		return nil
	}
	return searchKnowledgeDocuments(docs, prompt, limit, 1200)
}

func SearchFS(prompt string, fsys fs.ReadFileFS, paths []string, sourcePrefix string, limit int, maxExcerptChars int) []Match {
	if limit <= 0 {
		limit = 1
	}
	docs, err := loadKnowledgeDocumentsFromPaths(fsys, paths, sourcePrefix)
	if err != nil || len(docs) == 0 {
		return nil
	}
	return searchKnowledgeDocuments(docs, prompt, limit, maxExcerptChars)
}

func searchKnowledgeDocuments(docs []knowledgeDocument, prompt string, limit int, maxExcerptChars int) []Match {
	terms := tokenizeKnowledgePrompt(prompt)
	if len(terms) == 0 {
		terms = []string{"tunnel-client"}
	}

	matches := make([]Match, 0, len(docs))
	for _, doc := range docs {
		best := Match{Path: doc.Path}
		for _, section := range doc.Sections {
			score := knowledgeScore(doc, section, terms)
			if score <= 0 {
				continue
			}
			if score > best.Score {
				best.Score = score
				best.Heading = section.Heading
				best.Excerpt = excerptKnowledgeSection(section, terms, maxExcerptChars)
			}
		}
		if best.Score > 0 {
			matches = append(matches, best)
		}
	}
	if len(matches) == 0 {
		return nil
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Path < matches[j].Path
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func loadKnowledgeDocuments() ([]knowledgeDocument, error) {
	knowledgeDocsOnce.Do(func() {
		paths := make([]string, 0, 16)
		topLevel, err := fs.Glob(knowledgeFS, "*.md")
		if err != nil {
			knowledgeDocsErr = err
			return
		}
		deployment, err := fs.Glob(knowledgeFS, "deployment/*.md")
		if err != nil {
			knowledgeDocsErr = err
			return
		}
		paths = append(paths, topLevel...)
		paths = append(paths, deployment...)
		sort.Strings(paths)
		knowledgeDocs, knowledgeDocsErr = loadKnowledgeDocumentsFromPaths(knowledgeFS, paths, "docs/")
	})
	return knowledgeDocs, knowledgeDocsErr
}

func loadKnowledgeDocumentsFromPaths(fsys fs.ReadFileFS, paths []string, sourcePrefix string) ([]knowledgeDocument, error) {
	docs := make([]knowledgeDocument, 0, len(paths))
	for _, path := range paths {
		data, err := fsys.ReadFile(path)
		if err != nil {
			return nil, err
		}
		displayPath := sourcePrefix + path
		sections := splitKnowledgeSections(string(data))
		if len(sections) == 0 {
			continue
		}
		title := sections[0].Heading
		if title == "" {
			title = displayPath
		}
		docs = append(docs, knowledgeDocument{
			Path:       displayPath,
			PathLower:  strings.ToLower(displayPath),
			Title:      title,
			TitleLower: strings.ToLower(title),
			Sections:   sections,
		})
	}
	return docs, nil
}

func splitKnowledgeSections(body string) []knowledgeSection {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	sections := make([]knowledgeSection, 0, 16)

	currentHeading := ""
	currentLines := make([]string, 0, len(lines))
	flush := func() {
		text := strings.TrimSpace(strings.Join(currentLines, "\n"))
		if text == "" {
			currentLines = currentLines[:0]
			return
		}
		sections = append(sections, knowledgeSection{
			Heading:      currentHeading,
			HeadingLower: strings.ToLower(currentHeading),
			Body:         text,
			SearchText:   strings.ToLower(text),
		})
		currentLines = currentLines[:0]
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			flush()
			currentHeading = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		}
		currentLines = append(currentLines, line)
	}
	flush()
	return sections
}

func tokenizeKnowledgePrompt(prompt string) []string {
	raw := knowledgeTokenRE.FindAllString(strings.ToLower(prompt), -1)
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	terms := make([]string, 0, len(raw))
	for _, token := range raw {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if _, stop := knowledgeStopwords[token]; stop {
			continue
		}
		if len(token) < 3 {
			if _, ok := knowledgeShortAllowlist[token]; !ok {
				continue
			}
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		terms = append(terms, token)
	}
	return terms
}

func knowledgeScore(doc knowledgeDocument, section knowledgeSection, terms []string) int {
	score := 0
	for _, term := range terms {
		if strings.Contains(doc.PathLower, term) {
			score += 20
		}
		if strings.Contains(doc.TitleLower, term) {
			score += 24
		}
		if strings.Contains(section.HeadingLower, term) {
			score += 28
		}
		if count := strings.Count(section.SearchText, term); count > 0 {
			if count > 6 {
				count = 6
			}
			score += count * 5
		}
	}
	if strings.Contains(section.HeadingLower, "chatgpt") && containsKnowledgeTerm(terms, "chatgpt") {
		score += 12
	}
	if strings.Contains(section.HeadingLower, "troubleshooting") && containsAnyKnowledgeTerm(terms, "debug", "diagnose", "troubleshoot", "healthz", "readyz", "logs", "log") {
		score += 12
	}
	return score
}

func containsKnowledgeTerm(terms []string, target string) bool {
	for _, term := range terms {
		if term == target {
			return true
		}
	}
	return false
}

func containsAnyKnowledgeTerm(terms []string, targets ...string) bool {
	for _, target := range targets {
		if containsKnowledgeTerm(terms, target) {
			return true
		}
	}
	return false
}

func excerptKnowledgeSection(section knowledgeSection, terms []string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 1200
	}
	text := strings.TrimSpace(section.Body)
	if len(text) <= maxChars {
		return text
	}

	lower := strings.ToLower(text)
	best := -1
	for _, term := range terms {
		if idx := strings.Index(lower, term); idx >= 0 && (best == -1 || idx < best) {
			best = idx
		}
	}

	start := 0
	if best > 0 {
		start = best - maxChars/4
		if start < 0 {
			start = 0
		}
	}
	end := start + maxChars
	if end > len(text) {
		end = len(text)
		start = max(0, end-maxChars)
	}
	snippet := strings.TrimSpace(text[start:end])
	if start > 0 {
		snippet = "...\n" + snippet
	}
	if end < len(text) {
		snippet += "\n..."
	}
	return snippet
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
