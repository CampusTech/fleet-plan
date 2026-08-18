package merge

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMergeFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    string
		overlay string
		want    map[string]any
		wantErr bool
	}{
		{
			name:    "basic merge: disjoint keys",
			base:    "a: 1\n",
			overlay: "b: 2\n",
			want:    map[string]any{"a": 1, "b": 2},
		},
		{
			name:    "overlay wins on same key",
			base:    "key: base_value\n",
			overlay: "key: overlay_value\n",
			want:    map[string]any{"key": "overlay_value"},
		},
		{
			name:    "deep merge: nested maps",
			base:    "parent:\n  child_a: 1\n  child_b: 2\n",
			overlay: "parent:\n  child_b: 99\n  child_c: 3\n",
			want: map[string]any{
				"parent": map[string]any{
					"child_a": 1,
					"child_b": 99,
					"child_c": 3,
				},
			},
		},
		{
			name:    "overlay replaces scalar with map",
			base:    "key: scalar\n",
			overlay: "key:\n  nested: value\n",
			want:    map[string]any{"key": map[string]any{"nested": "value"}},
		},
		{
			name:    "empty overlay leaves base intact",
			base:    "a: 1\nb: 2\n",
			overlay: "{}\n",
			want:    map[string]any{"a": 1, "b": 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			basePath := filepath.Join(dir, "base.yml")
			overlayPath := filepath.Join(dir, "overlay.yml")
			outPath := filepath.Join(dir, "out.yml")

			if err := os.WriteFile(basePath, []byte(tt.base), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(overlayPath, []byte(tt.overlay), 0o644); err != nil {
				t.Fatal(err)
			}

			err := MergeFiles(basePath, overlayPath, outPath)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("MergeFiles: %v", err)
			}

			data, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("reading output: %v", err)
			}
			var got map[string]any
			if err := yaml.Unmarshal(data, &got); err != nil {
				t.Fatalf("parsing output YAML: %v", err)
			}

			assertMapEqual(t, tt.want, got)
		})
	}
}

func TestMergeFiles_MissingBase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	overlayPath := filepath.Join(dir, "overlay.yml")
	if err := os.WriteFile(overlayPath, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := MergeFiles(filepath.Join(dir, "nonexistent.yml"), overlayPath, filepath.Join(dir, "out.yml"))
	if err == nil {
		t.Fatal("expected error for missing base file")
	}
}

func TestMergeFiles_MissingOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yml")
	if err := os.WriteFile(basePath, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := MergeFiles(basePath, filepath.Join(dir, "nonexistent.yml"), filepath.Join(dir, "out.yml"))
	if err == nil {
		t.Fatal("expected error for missing overlay file")
	}
}

// assertMapEqual recursively compares two maps for value equality.
func assertMapEqual(t *testing.T, want, got map[string]any) {
	t.Helper()
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("missing key %q", k)
			continue
		}
		wMap, wIsMap := wv.(map[string]any)
		gMap, gIsMap := gv.(map[string]any)
		if wIsMap && gIsMap {
			assertMapEqual(t, wMap, gMap)
		} else {
			// yaml.v3 unmarshals integers as int, compare via sprintf
			if stringify(wv) != stringify(gv) {
				t.Errorf("key %q: got %v, want %v", k, gv, wv)
			}
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected key %q in result", k)
		}
	}
}

func stringify(v any) string {
	switch val := v.(type) {
	case string:
		return val
	default:
		b, _ := yaml.Marshal(val)
		return string(b)
	}
}
