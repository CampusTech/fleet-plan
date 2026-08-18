package diff

import (
	"crypto/md5" //nolint:gosec // Fleet's profile checksum is MD5; this only compares identity, never authenticates.
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// profileChecksum returns the base64 MD5 of content, which is the form Fleet
// reports as a profile's `checksum`. Matching checksums mean the stored profile
// is byte-identical to the local file, so no content needs to be downloaded.
func profileChecksum(content []byte) string {
	sum := md5.Sum(content) //nolint:gosec // identity comparison only; see import comment.
	return base64.StdEncoding.EncodeToString(sum[:])
}

// profileKeys flattens a profile into dot-separated key paths and their scalar
// values, so two versions can be compared key by key.
//
// Values are only ever compared, never rendered: a profile can carry
// certificates, passwords, and enroll secrets, and this output reaches CI logs
// and MR comments.
func profileKeys(content []byte) (map[string]string, error) {
	trimmed := strings.TrimSpace(string(content))
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return jsonProfileKeys([]byte(trimmed))
	}
	return plistKeys(content)
}

// jsonProfileKeys flattens a DDM declaration (.json).
func jsonProfileKeys(content []byte) (map[string]string, error) {
	var v any
	if err := json.Unmarshal(content, &v); err != nil {
		return nil, fmt.Errorf("parsing JSON declaration: %w", err)
	}
	keys := make(map[string]string)
	flattenValue("", v, keys)
	return keys, nil
}

// flattenValue walks a decoded JSON value into path → scalar entries.
func flattenValue(path string, v any, out map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		for k, sub := range val {
			flattenValue(joinPath(path, k), sub, out)
		}
	case []any:
		for i, sub := range val {
			flattenValue(fmt.Sprintf("%s[%d]", path, i), sub, out)
		}
	default:
		out[path] = fmt.Sprint(v)
	}
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// plistKeys flattens an XML property list (.mobileconfig, and Windows .xml
// profiles that happen to be plists) into path → scalar entries.
//
// The grammar is small: a <dict> alternates <key> and value elements, an
// <array> holds positional values, and the rest are scalars. Anything that is
// not a plist at all returns an error so the caller can fall back to reporting
// "contents differ" without naming keys.
func plistKeys(content []byte) (map[string]string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(content)))
	dec.Strict = false

	keys := make(map[string]string)

	// Container stack. A dict tracks the key most recently read; an array
	// tracks how many values it has seen so far.
	type frame struct {
		path    string
		isArray bool
		key     string // pending dict key
		index   int    // next array index
	}
	var stack []frame
	sawPlistRoot := false

	// nextPath returns the path for the value about to be read, consuming the
	// pending dict key or array index.
	nextPath := func() string {
		if len(stack) == 0 {
			return ""
		}
		f := &stack[len(stack)-1]
		if f.isArray {
			p := fmt.Sprintf("%s[%d]", f.path, f.index)
			f.index++
			return p
		}
		p := joinPath(f.path, f.key)
		f.key = ""
		return p
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing plist: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "plist":
				sawPlistRoot = true
			case "dict", "array":
				path := ""
				if len(stack) > 0 {
					path = nextPath()
				}
				stack = append(stack, frame{path: path, isArray: t.Name.Local == "array"})
			case "key":
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return nil, fmt.Errorf("parsing plist key: %w", err)
				}
				if len(stack) > 0 {
					stack[len(stack)-1].key = s
				}
			case "true", "false":
				keys[nextPath()] = t.Name.Local
			case "string", "integer", "real", "data", "date":
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return nil, fmt.Errorf("parsing plist value: %w", err)
				}
				keys[nextPath()] = strings.TrimSpace(s)
			}
		case xml.EndElement:
			if (t.Name.Local == "dict" || t.Name.Local == "array") && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}

	if !sawPlistRoot {
		return nil, fmt.Errorf("not a property list")
	}
	return keys, nil
}

// profileKeyChanges compares two flattened profiles and returns the changed
// key paths, marked "+" for keys only in the proposed profile and "-" for keys
// only in the live one. Keys whose proposed value contains a "$" placeholder
// are skipped: Fleet substitutes those server-side, so the stored value never
// matches the file and the difference is not a real change.
func profileKeyChanges(current, proposed map[string]string) []string {
	var changed []string
	for k, proposedVal := range proposed {
		if containsEnvVar(proposedVal) {
			continue
		}
		curVal, ok := current[k]
		if !ok {
			changed = append(changed, "+"+k)
			continue
		}
		if curVal != proposedVal {
			changed = append(changed, k)
		}
	}
	for k := range current {
		if _, ok := proposed[k]; !ok {
			changed = append(changed, "-"+k)
		}
	}
	sort.Strings(changed)
	return changed
}

// profileDiffSummary renders changed key paths for display. Only key names are
// shown; values never are. Long lists are truncated so one profile cannot
// flood a CI comment.
func profileDiffSummary(changed []string) string {
	const maxKeys = 5
	if len(changed) == 0 {
		return ""
	}
	shown := changed
	suffix := ""
	if len(changed) > maxKeys {
		shown = changed[:maxKeys]
		suffix = ", +" + strconv.Itoa(len(changed)-maxKeys) + " more"
	}
	noun := "keys"
	if len(changed) == 1 {
		noun = "key"
	}
	return fmt.Sprintf("%d %s changed: %s%s", len(changed), noun, strings.Join(shown, ", "), suffix)
}
