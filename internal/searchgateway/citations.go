package searchgateway

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"account-pool-balancer/internal/websearch"
)

// citedPart adds evidence links only when their exact, previously fetched URL
// is literally present in the answer. It never rewrites text or invents claims.
// Offsets are Unicode character indices (exclusive end), not UTF-8 byte counts.
func citedPart(part object, sources []websearch.Source) (object, []json.RawMessage) {
	if str(part["type"]) != "output_text" {
		return part, nil
	}
	text := str(part["text"])
	if text == "" || len(sources) == 0 {
		return part, nil
	}
	existing, err := array(part["annotations"])
	if part["annotations"] != nil && string(part["annotations"]) != "null" && err != nil {
		return part, nil
	}
	type citation struct {
		start, end int
		source     websearch.Source
	}
	found := []citation{}
	seen := map[string]bool{}
	for _, s := range sources {
		if s.URL == "" || seen[s.URL] {
			continue
		}
		seen[s.URL] = true
		for offset := 0; offset < len(text) && len(found) < 64; {
			at := strings.Index(text[offset:], s.URL)
			if at < 0 {
				break
			}
			at += offset
			end := at + len(s.URL)
			offset = end
			// A URL embedded in another URL's query/path is not a link to this
			// source. Nor is a known URL merely a prefix of an unrelated URL.
			if at > 0 {
				before, _ := utf8.DecodeLastRuneInString(text[:at])
				if !(unicode.IsSpace(before) || strings.ContainsRune("([<\"'：", before)) {
					continue
				}
			}
			if end < len(text) {
				r, _ := utf8.DecodeRuneInString(text[end:])
				if !(unicode.IsSpace(r) || strings.ContainsRune(")]}>\"'，。；！、", r)) {
					continue
				}
			}
			found = append(found, citation{utf8.RuneCountInString(text[:at]), utf8.RuneCountInString(text[:end]), s})
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].start == found[j].start {
			return found[i].end < found[j].end
		}
		return found[i].start < found[j].start
	})
	added := []json.RawMessage{}
	for _, c := range found {
		duplicate := false
		for _, raw := range existing {
			o, e := decodeObject(raw)
			if e != nil {
				continue
			}
			if str(o["type"]) == "url_citation" && str(o["url"]) == c.source.URL {
				start, ok := intField(o, "start_index")
				end, ok2 := intField(o, "end_index")
				if ok && ok2 && start == int64(c.start) && end == int64(c.end) {
					duplicate = true
					break
				}
			}
		}
		if duplicate {
			continue
		}
		annotation := marshal(map[string]any{"type": "url_citation", "url": c.source.URL, "title": c.source.Title, "start_index": c.start, "end_index": c.end})
		added = append(added, annotation)
		existing = append(existing, annotation)
	}
	if len(added) == 0 {
		return part, nil
	}
	out := clone(part)
	out["annotations"] = marshal(existing)
	return out, added
}
func (e *emitter) citedItem(item object) object {
	if str(item["type"]) != "message" {
		return item
	}
	parts, err := array(item["content"])
	if err != nil {
		return item
	}
	changed := false
	for i, raw := range parts {
		part, err := decodeObject(raw)
		if err != nil {
			continue
		}
		updated, added := citedPart(part, e.sources)
		if len(added) > 0 {
			parts[i] = marshal(updated)
			changed = true
		}
	}
	if !changed {
		return item
	}
	out := clone(item)
	out["content"] = marshal(parts)
	return out
}
func (r *roundEmitter) annotatePart(index, ci int, part object) object {
	if r.parts[index] == nil {
		r.parts[index] = map[int]object{}
	}
	if previous := r.parts[index][ci]; previous != nil {
		return previous
	}
	updated, added := citedPart(part, r.e.sources)
	r.parts[index][ci] = updated
	old := 0
	if list, e := array(part["annotations"]); e == nil {
		old = len(list)
	}
	for i, a := range added {
		r.e.event("response.output_text.annotation.added", object{"output_index": marshal(r.indices[index]), "item_id": marshal(r.publicID(index)), "content_index": marshal(ci), "annotation_index": marshal(old + i), "annotation": a})
	}
	return updated
}
func (r *roundEmitter) annotateItem(index int, item object) {
	if str(item["type"]) != "message" {
		return
	}
	parts, err := array(item["content"])
	if err != nil {
		return
	}
	for ci, raw := range parts {
		part, err := decodeObject(raw)
		if err == nil {
			r.annotatePart(index, ci, part)
		}
	}
}
