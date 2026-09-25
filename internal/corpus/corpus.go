// Package corpus adapts internal/vault (notes and their OCR sidecars)
// into the internal/search.Doc shape.
package corpus

import (
	"context"
	"errors"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/lamovs/nn/internal/ocr"
	"github.com/lamovs/nn/internal/search"
	"github.com/lamovs/nn/internal/vault"
)

// ErrNotImplemented marks functionality this package only stubs out.
var ErrNotImplemented = errors.New("not implemented")

// Load builds a Doc for every note plus every unembedded image, path order.
func Load(ctx context.Context, v *vault.Vault) ([]*search.Doc, error) {
	var docs []*search.Doc
	if err := Stream(ctx, v, func(d *search.Doc) error {
		docs = append(docs, d)
		return nil
	}); err != nil {
		return nil, err
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	return docs, nil
}

// Stream is Load's incremental counterpart, calling fn as each Doc is ready.
func Stream(ctx context.Context, v *vault.Vault, fn func(*search.Doc) error) error {
	if v == nil {
		return errors.New("no vault")
	}
	embedded := map[string]bool{}
	err := v.LoadEach(ctx, func(n *vault.Note) error {
		for _, img := range n.Embeds {
			embedded[strings.ToLower(img)] = true
		}
		return fn(noteDoc(v, n))
	})
	if err != nil {
		return err
	}

	images, err := v.Images(ctx)
	if err != nil {
		return err
	}
	for _, img := range images {
		if embedded[strings.ToLower(img)] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(imageDoc(v, img)); err != nil {
			return err
		}
	}
	return nil
}

func noteDoc(v *vault.Vault, n *vault.Note) *search.Doc {
	doc := &search.Doc{
		Path:     n.Path,
		Title:    n.Title,
		Aliases:  n.Aliases,
		Tags:     n.Tags,
		Date:     n.Date,
		Modified: n.Modified,
		Where:    n.Where,
		Repo:     n.Repo,
		InInbox:  n.InInbox,
		Via:      n.Via,
		Lines:    bodyLines(n),
	}
	for _, img := range n.Embeds {
		doc.Images = append(doc.Images, search.ImageDoc{Path: img, Lines: sidecarLines(v, img)})
	}
	return doc
}

func bodyLines(n *vault.Note) []search.Line {
	var lines []search.Line
	for i, line := range strings.Split(strings.TrimSuffix(n.Body, "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, search.Line{Num: n.BodyStartLine + i, Text: line})
	}
	return lines
}

func imageDoc(v *vault.Vault, rel string) *search.Doc {
	doc := &search.Doc{
		Path:    rel,
		Title:   path.Base(rel),
		InInbox: v.InInbox(rel),
		Images:  []search.ImageDoc{{Path: rel, Lines: sidecarLines(v, rel)}},
	}
	if info, err := os.Stat(v.Abs(rel)); err == nil {
		doc.Modified = info.ModTime()
	}
	return doc
}

func sidecarLines(v *vault.Vault, rel string) []ocr.Line {
	result, err := ocr.LoadSidecar(v.Root, rel)
	if err != nil {
		return nil
	}
	return result.Lines
}
