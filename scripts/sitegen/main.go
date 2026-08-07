// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// pageData contains values rendered into the HTML page template.
type pageData struct {
	Title      string
	Stylesheet string
	Home       string
	Body       template.HTML
}

// main renders the documentation website.
func main() {
	out := flag.String("out", "dist/website", "output directory")
	flag.Parse()

	if err := build(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// build renders README.md and every Markdown document under docs.
func build(out string) error {
	files, err := filepath.Glob("docs/*.md")
	if err != nil {
		return err
	}
	sort.Strings(files)
	files = append([]string{"README.md"}, files...)

	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(out, "docs"), 0o755); err != nil {
		return err
	}
	style, err := os.ReadFile("scripts/sitegen/style.css")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "style.css"), style, 0o644); err != nil {
		return err
	}
	license, err := os.ReadFile("LICENSE")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "LICENSE"), license, 0o644); err != nil {
		return err
	}
	page, err := template.ParseFiles("scripts/sitegen/page.html")
	if err != nil {
		return err
	}

	for _, name := range files {
		if err := render(page, out, name); err != nil {
			return fmt.Errorf("render %s: %w", name, err)
		}
	}
	return nil
}

// render converts one Markdown document into a complete HTML page.
func render(page *template.Template, out, name string) error {
	source, err := os.ReadFile(name)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	if err := markdown.Convert(source, &body); err != nil {
		return err
	}

	title := "Podmin"
	for _, line := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# ")) + " · Podmin"
			break
		}
	}

	destination := "index.html"
	prefix := ""
	if name != "README.md" {
		destination = strings.TrimSuffix(name, ".md") + ".html"
		prefix = "../"
	}

	path := filepath.Join(out, destination)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	content := strings.ReplaceAll(body.String(), ".md\"", ".html\"")
	content = strings.ReplaceAll(content, `href="./LICENSE"`, `href="./LICENSE" target="_blank" rel="noopener noreferrer"`)
	var output bytes.Buffer
	if err := page.Execute(&output, pageData{
		Title:      title,
		Stylesheet: prefix + "style.css",
		Home:       prefix + "index.html",
		Body:       template.HTML(content),
	}); err != nil {
		return err
	}
	return os.WriteFile(path, output.Bytes(), 0o644)
}
